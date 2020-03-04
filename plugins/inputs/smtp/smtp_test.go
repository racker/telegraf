package smtp

import (
	"bufio"
	"crypto/tls"
	internaltls "github.com/influxdata/telegraf/internal/tls"
	"io"
	"net"
	"net/textproto"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/influxdata/telegraf/internal"
	"github.com/influxdata/telegraf/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var pki = testutil.NewPKI("../../../testutil/pki")

type testConfig struct {
	// defines the step at which the connection should close
	// the connection will close directly before the given step is executed
	connectionEndPhase ConnectionEndPhase
	tls                bool
	tlsInsecure        bool
}

type ConnectionEndPhase int

const (
	ConnectionTimeout ConnectionEndPhase = iota + 1
	LateTimeout
	FailEhlo
	FailFrom
	FailTo
	FailData
	FailPayload
	FailQuit
)

func TestSample(t *testing.T) {
	c := &Smtp{}
	output := c.SampleConfig()
	if output != sampleConfig {
		t.Error("Sample config doesn't match")
	}
}

func TestDescription(t *testing.T) {
	c := &Smtp{}
	output := c.Description()
	if output != description {
		t.Error("Description output is not correct")
	}
}

func TestNoPort(t *testing.T) {
	var acc testutil.Accumulator
	c := Smtp{
		Address: ":",
	}
	err1 := c.Gather(&acc)
	require.Error(t, err1)
	assert.Equal(t, "Bad port", err1.Error())
}

func TestAddressOnly(t *testing.T) {
	var acc testutil.Accumulator
	c := Smtp{
		Address: "127.0.0.1",
	}
	err1 := c.Gather(&acc)
	require.Error(t, err1)
	assert.Equal(t, "address 127.0.0.1: missing port in address", err1.Error())
}

func TestConnectionError(t *testing.T) {
	var acc testutil.Accumulator
	// Init plugin
	c := getDefaultSmtpConfig()
	// Error
	err1 := c.Gather(&acc)
	for _, p := range acc.Metrics {
		p.Fields["connect_time"] = 1.0
		p.Fields["total_time"] = 2.0
	}
	require.NoError(t, err1)
	acc.AssertContainsTaggedFields(t,
		"smtp",
		map[string]interface{}{
			"result_code":  uint64(2),
			"connect_time": 1.0,
			"total_time":   2.0,
		},
		map[string]string{
			"result": "connection_failed",
			"server": "127.0.0.1",
			"port":   "2004",
		},
	)
}

func testSmtpHelper(t *testing.T, testConfig testConfig, fields map[string]interface{}, tags map[string]string) {
	var wg sync.WaitGroup
	var acc testutil.Accumulator
	// Init plugin
	c := getDefaultSmtpConfig()
	if testConfig.tls {
		c = getTlsSmtp(testConfig.tlsInsecure)
	}

	// Start TCP server
	wg.Add(1)
	go SmtpServer(t, &wg, testConfig)
	wg.Wait()
	// Connect
	wg.Add(1)
	err1 := c.Gather(&acc)
	wg.Wait()
	// Override response time
	for _, p := range acc.Metrics {
		p.Fields["connect_time"] = 1.0
		p.Fields["total_time"] = 2.0
	}
	require.NoError(t, err1)
	acc.AssertContainsTaggedFields(t, "smtp", fields, tags)
}

func TestSmtpFullSession_Success(t *testing.T) {
	fields, tags := getFieldsAndTags("success", 0, false, 220, 250, 250, 250, 354, 250, 221)
	testSmtpHelper(t, testConfig{}, fields, tags)
}

func TestSmtpTlsSession_Success(t *testing.T) {
	fields, tags := getFieldsAndTags("success", 0, true, 220, 250, 220, 250, 250, 354, 250, 221)
	testConfig := testConfig{
		connectionEndPhase: 0,
		tls:                true,
		tlsInsecure:        true,
	}
	testSmtpHelper(t, testConfig, fields, tags)
}

func TestSmtpSecureTlsSession_Fail(t *testing.T) {
	fields, tags := getFieldsAndTags("tls_config_error", 5, true, 220, 250)
	testConfig := testConfig{
		connectionEndPhase: 0,
		tls:                true,
		tlsInsecure:        false,
	}
	testSmtpHelper(t, testConfig, fields, tags)
}

func TestSmtp_FailTimeoutConnection(t *testing.T) {
	fields, tags := getFieldsAndTags("timeout", 1, false)
	testConfig := testConfig{connectionEndPhase: ConnectionTimeout}
	testSmtpHelper(t, testConfig, fields, tags)
}

func TestSmtp_FailTimeoutAfterRcptTo(t *testing.T) {
	fields, tags := getFieldsAndTags("timeout", 1, false, 220, 250, 250, 250)
	testConfig := testConfig{connectionEndPhase: LateTimeout}
	testSmtpHelper(t, testConfig, fields, tags)
}

func TestSmtp_FailEhlo(t *testing.T) {
	fields, tags := getFieldsAndTags("string_mismatch", 4, false, 220, 421)
	testConfig := testConfig{connectionEndPhase: FailEhlo}
	testSmtpHelper(t, testConfig, fields, tags)
}

func TestSmtp_FailFrom(t *testing.T) {
	fields, tags := getFieldsAndTags("string_mismatch", 4, false, 220, 250, 423)
	testConfig := testConfig{connectionEndPhase: FailFrom}
	testSmtpHelper(t, testConfig, fields, tags)
}

func TestSmtp_FailTo(t *testing.T) {
	fields, tags := getFieldsAndTags("string_mismatch", 4, false, 220, 250, 250, 424)
	testConfig := testConfig{connectionEndPhase: FailTo}
	testSmtpHelper(t, testConfig, fields, tags)
}

func TestSmtp_FailData(t *testing.T) {
	fields, tags := getFieldsAndTags("string_mismatch", 4, false, 220, 250, 250, 250, 425)
	testConfig := testConfig{connectionEndPhase: FailData}
	testSmtpHelper(t, testConfig, fields, tags)
}

func TestSmtp_FailPayload(t *testing.T) {
	fields, tags := getFieldsAndTags("string_mismatch", 4, false, 220, 250, 250, 250, 354, 425)
	testConfig := testConfig{connectionEndPhase: FailPayload}
	testSmtpHelper(t, testConfig, fields, tags)
}

// Rather than closing the connection when failing here, we instead get an unexpected response code
func TestSmtp_FailQuit(t *testing.T) {
	fields, tags := getFieldsAndTags("string_mismatch", 4, false, 220, 250, 250, 250, 354, 250, 426)
	testConfig := testConfig{connectionEndPhase: FailQuit}
	testSmtpHelper(t, testConfig, fields, tags)
}

// codes must be provided in the same order as the codeTypes array
func getFieldsAndTags(status string, result int, tls bool, codes ...int) (fields map[string]interface{}, tags map[string]string) {
	codeTypes := []string{
		"connect_code",
		"ehlo_code",
		"starttls_code",
		"from_code",
		"to_code",
		"data_code",
		"body_code",
		"quit_code",
	}

	fields = map[string]interface{}{
		"result_code":  uint64(result),
		"connect_time": 1.0,
		"total_time":   2.0,
	}
	tags = map[string]string{
		"result": status,
		"server": "127.0.0.1",
		"port":   "2004",
	}

	// populate the codes provided into the corresponding metric name
	// codes are only provided if that step is executed
	// the last code is always for "quit"
	for i, code := range codes {
		if i > 1 && !tls {
			fields[codeTypes[i+1]] = code
		} else {
			fields[codeTypes[i]] = code
		}
	}

	return fields, tags
}

//noinspection GoUnhandledErrorResult
func SmtpServer(t *testing.T, wg *sync.WaitGroup, config testConfig) {

	tcpServer, err := net.Listen("tcp", "127.0.0.1:2004")
	require.NoError(t, err)
	defer tcpServer.Close()
	wg.Done()

	conn, err := tcpServer.Accept()
	require.NoError(t, err)
	defer conn.Close()

	reader := bufio.NewReader(conn)
	tp := textproto.NewReader(reader)

	if config.connectionEndPhase == ConnectionTimeout {
		time.Sleep(getDefaultSmtpConfig().ReadTimeout.Duration + time.Second)
		wg.Done()
		return
	}

	// send initial connection response
	conn.Write([]byte("220 myhostname ESMTP Postfix (Ubuntu)\r\n"))

	for {
		data, err := tp.ReadLine()
		if err != nil {
			if err == io.EOF {
				// if the client disconnected, exit to close the server connection
				break
			}
			if opErr, ok := err.(*net.OpError); ok {
				if syscallErr, ok := opErr.Err.(*os.SyscallError); ok {
					if syscallErr.Err == syscall.ECONNRESET {
						break
					}
				}
			}
		}
		assert.NoError(t, err, "Unhandled error occurred")

		// quit must be handled before other cases since most failures will trigger a quit command
		// i.e. if FailEhlo is executed we still need to handle receiving a quit
		// the other responses are ordered based on the order of execution in the plugin
		if strings.HasPrefix(data, "QUIT") {
			if config.connectionEndPhase == FailQuit {
				conn.Write([]byte("426 This is a fake error\r\n"))
			} else {
				conn.Write([]byte("221 2.0.0 Bye\r\n"))
			}
		} else if config.connectionEndPhase == FailEhlo {
			conn.Write([]byte("421 This is a fake error\r\n"))
		} else if strings.HasPrefix(data, "EHLO") {
			conn.Write([]byte("250-myhostname\r\n"))
			conn.Write([]byte("250-PIPELINING\r\n"))
			conn.Write([]byte("250-SIZE 10240000\r\n"))
			conn.Write([]byte("250-VRFY\r\n"))
			conn.Write([]byte("250-ETRN\r\n"))
			conn.Write([]byte("250-STARTTLS\r\n"))
			conn.Write([]byte("250-ENHANCEDSTATUSCODES\r\n"))
			conn.Write([]byte("250-8BITMIME\r\n"))
			conn.Write([]byte("250-DSN\r\n"))
			conn.Write([]byte("250 SMTPUTF8\r\n"))
		} else if strings.HasPrefix(data, "STARTTLS") {
			if config.tls {
				conn.Write([]byte("220 2.1.0 Ok\r\n"))
				tlsConf := getTlsServerConfig()
				tlsConn := tls.Server(conn, tlsConf)
				tlsConn.Handshake()
				// update connection and reader
				conn = net.Conn(tlsConn)
				reader := bufio.NewReader(conn)
				tp = textproto.NewReader(reader)
			}
		} else if config.connectionEndPhase == FailFrom {
			conn.Write([]byte("423 This is a fake error\r\n"))
		} else if strings.HasPrefix(data, "MAIL FROM:") {
			conn.Write([]byte("250 2.1.0 Ok\r\n"))
		} else if config.connectionEndPhase == FailTo {
			conn.Write([]byte("424 This is a fake error\r\n"))
		} else if strings.HasPrefix(data, "RCPT TO:") {
			conn.Write([]byte("250 2.1.5 Ok\r\n"))
		} else if config.connectionEndPhase == LateTimeout {
			time.Sleep(getDefaultSmtpConfig().ReadTimeout.Duration + time.Second)
			wg.Done()
			return
		} else if config.connectionEndPhase == FailData {
			conn.Write([]byte("425 This is a fake error\r\n"))
		} else if strings.HasPrefix(data, "DATA") {
			conn.Write([]byte("354 End data with <CR><LF>.<CR><LF>\r\n"))
		} else if config.connectionEndPhase == FailPayload {
			conn.Write([]byte("425 This is a fake error\r\n"))
		} else if strings.HasPrefix(data, "testdata") {
			conn.Write([]byte("250 2.0.0 Ok: queued as C7CAA3F279\r\n"))
		}
	}
	wg.Done()
}

func getDefaultSmtpConfig() Smtp {
	return Smtp{
		Address:     "127.0.0.1:2004",
		Timeout:     internal.Duration{Duration: time.Second},
		ReadTimeout: internal.Duration{Duration: time.Second * 2},
		Ehlo:        "me@test.com",
		From:        "me2@test.com",
		To:          "me3@test.com",
		Body:        "testdata 12345",
		StartTls:    false,
	}
}

func getTlsSmtp(insecure bool) Smtp {
	conf := getDefaultSmtpConfig()
	conf.StartTls = true
	conf.ClientConfig = *getTlsClientConfig(insecure)

	return conf
}

func getTlsServerConfig() *tls.Config {
	pair, _ := tls.X509KeyPair([]byte(pki.ReadServerCert()), []byte(pki.ReadServerKey()))

	config := &tls.Config{
		InsecureSkipVerify: false,
		Certificates:       []tls.Certificate{pair},
	}
	return config
}

func getTlsClientConfig(insecure bool) *internaltls.ClientConfig {
	return &internaltls.ClientConfig{
		InsecureSkipVerify: insecure,
	}
}
