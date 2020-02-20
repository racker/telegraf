package smtp

import (
	"bufio"
	"net"
	"net/textproto"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/influxdata/telegraf/internal"
	"github.com/influxdata/telegraf/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type serverConfig struct {
	// defines the step at which the connection should close
	// the connection will close directly before the given step is executed
	connectionEndPhase ConnectionEndPhase
}

type ConnectionEndPhase int

const (
	ConnectionTimeout ConnectionEndPhase = iota + 1
	Ehlo
	From
	To
	Data
	Payload
	Quit
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
	}
	require.NoError(t, err1)
	acc.AssertContainsTaggedFields(t,
		"smtp",
		map[string]interface{}{
			"result_code":  uint64(2),
			"connect_time": 1.0,
		},
		map[string]string{
			"result": "connection_failed",
			"server": "127.0.0.1",
			"port":   "2004",
		},
	)
}

func testSmtpHelper(t *testing.T, phase ConnectionEndPhase, fields map[string]interface{}, tags map[string]string) {
	var wg sync.WaitGroup
	var acc testutil.Accumulator
	// Init plugin
	c := getDefaultSmtpConfig()

	// Start TCP server
	wg.Add(1)
	go SmtpServer(t, &wg, serverConfig{
		connectionEndPhase: phase,
	})
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
	// Waiting TCPserver
	wg.Wait()
}

func TestSmtpFullSession_Success(t *testing.T) {
	fields, tags := getFieldsAndTags("success", 0, 220, 250, 250, 250, 354, 250, 221)
	testSmtpHelper(t, 0, fields, tags)
}

func TestSmtp_FailTimeout(t *testing.T) {
	fields, tags := getFieldsAndTags("timeout", 1, 0, 0)
	testSmtpHelper(t, ConnectionTimeout, fields, tags)
}

func TestSmtp_FailEhlo(t *testing.T) {
	fields, tags := getFieldsAndTags("read_failed", 3, 220, 0, 0)
	testSmtpHelper(t, Ehlo, fields, tags)
}

func TestSmtp_FailFrom(t *testing.T) {
	fields, tags := getFieldsAndTags("read_failed", 3, 220, 250, 0, 0)
	testSmtpHelper(t, From, fields, tags)
}

func TestSmtp_FailTo(t *testing.T) {
	fields, tags := getFieldsAndTags("read_failed", 3, 220, 250, 250, 0, 0)
	testSmtpHelper(t, To, fields, tags)
}

func TestSmtp_FailData(t *testing.T) {
	fields, tags := getFieldsAndTags("read_failed", 3, 220, 250, 250, 250, 0, 0)
	testSmtpHelper(t, Data, fields, tags)
}

func TestSmtp_FailPayload(t *testing.T) {
	fields, tags := getFieldsAndTags("read_failed", 3, 220, 250, 250, 250, 354, 0, 0)
	testSmtpHelper(t, Payload, fields, tags)
}

// Rather than closing the connection when failing here, we instead get an unexpected response code
func TestSmtp_FailQuit(t *testing.T) {
	fields, tags := getFieldsAndTags("string_mismatch", 4, 220, 250, 250, 250, 354, 250, 999)
	testSmtpHelper(t, Quit, fields, tags)
}

// codes must be provided in the same order as the codeTypes array
func getFieldsAndTags(status string, result int, codes ...int) (fields map[string]interface{}, tags map[string]string) {
	codeTypes := []string{
		"connect_code",
		"ehlo_code",
		"from_code",
		"to_code",
		"data_code",
		"body_code",
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
		if i == len(codes)-1 {
			fields["quit_code"] = code
		} else {
			fields[codeTypes[i]] = code
		}

	}

	return fields, tags
}

//noinspection GoUnhandledErrorResult
func SmtpServer(t *testing.T, wg *sync.WaitGroup, resp serverConfig) {
	tcpAddr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:2004")
	tcpServer, _ := net.ListenTCP("tcp", tcpAddr)
	wg.Done()
	conn, _ := tcpServer.AcceptTCP()
	reader := bufio.NewReader(conn)
	tp := textproto.NewReader(reader)

	if resp.connectionEndPhase == ConnectionTimeout {
		time.Sleep(getDefaultSmtpConfig().ReadTimeout.Duration + 1*time.Second)
		conn.Close()
		tcpServer.Close()
		wg.Done()
		return
	}

	conn.Write([]byte("220 myhostname ESMTP Postfix (Ubuntu)\r\n"))

	for {
		data, err := tp.ReadLine()
		if err != nil {
			t.Error(err)
			break
		}
		if resp.connectionEndPhase == Ehlo {
			conn.Close()
			break
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
		} else if resp.connectionEndPhase == From {
			conn.Close()
			break
		} else if strings.HasPrefix(data, "MAIL FROM:") {
			conn.Write([]byte("250 2.1.0 Ok\r\n"))
		} else if resp.connectionEndPhase == To {
			conn.Close()
			break
		} else if strings.HasPrefix(data, "RCPT TO:") {
			conn.Write([]byte("250 2.1.5 Ok\r\n"))
		} else if resp.connectionEndPhase == Data {
			conn.Close()
			break
		} else if strings.HasPrefix(data, "DATA") {
			conn.Write([]byte("354 End data with <CR><LF>.<CR><LF>\r\n"))
		} else if resp.connectionEndPhase == Payload {
			conn.Close()
			break
		} else if strings.HasPrefix(data, "testdata") {
			conn.Write([]byte("250 2.0.0 Ok: queued as C7CAA3F279\r\n"))
		} else if resp.connectionEndPhase == Quit {
			conn.Write([]byte("999 This is a fake error\r\n"))
			break
		} else if strings.HasPrefix(data, "QUIT") {
			conn.Write([]byte("221 2.0.0 Bye\r\n"))
			break
		}
	}
	tcpServer.Close()
	wg.Done()
}

func getDefaultSmtpConfig() Smtp {
	return Smtp{
		Address:     "127.0.0.1:2004",
		Timeout:     internal.Duration{Duration: time.Second},
		ReadTimeout: internal.Duration{Duration: time.Second * 3},
		Ehlo:        "me@test.com",
		From:        "me2@test.com",
		To:          "me3@test.com",
		Body:        "testdata 12345",
		Tls:         false,
	}
}
