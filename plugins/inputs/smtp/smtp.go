package smtp

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"net/textproto"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/internal"
	internaltls "github.com/influxdata/telegraf/internal/tls"
	"github.com/influxdata/telegraf/plugins/inputs"
)

type ResultType uint64
type Operation string

const (
	Success ResultType = iota
	Timeout
	ConnectionFailed
	ReadFailed
	StringMismatch
)

const (
	Connect Operation = "connect"
	Ehlo = "ehlo"
	StartTls = "starttls"
	MailFrom = "from"
	RcptTo = "to"
	Data = "data"
	Body = "body"
	Quit = "quit"
)

// Smtp struct
type Smtp struct {
	Address     string
	Timeout     internal.Duration
	ReadTimeout internal.Duration
	Ehlo        string
	From        string
	To          string
	Body        string
	StartTls         bool

	internaltls.ClientConfig
}

var description = "Automates an entire SMTP session and reports metrics"

// Description will return a short string to explain what the plugin does.
func (*Smtp) Description() string {
	return description
}

var sampleConfig = `
  ## Server address (default localhost)
  address = "localhost:25"

  ## Set initial connection timeout
  # timeout = "10s"

  ## Set read timeout
  # read_timeout = "10s"

  ## Optional value to provide to ehlo command 
  # ehlo = "example.com"

  ## Optional value to provide to mailfrom command 
  # from = "me@example.com"

  ## Optional value to provide to rcptto command
  # to = "you@example.com"

  ## Optional value to provide to data command
  # body = "this is a test payload"

  ## Optional tls config
  # tls = false
`

// SampleConfig will return a complete configuration example with details about each field.
func (*Smtp) SampleConfig() string {
	return sampleConfig
}

func CheckResponse(r *textproto.Reader, operation Operation, expectedCode int, fields map[string]interface{}, tags map[string]string) (success bool) {
	var err error
	code, _, err := r.ReadResponse(expectedCode)
	if err != nil {
		if e, ok := err.(net.Error); ok && e.Timeout() {
			setProtocolMetrics(Timeout, operation, code, fields, tags)
		} else if e, ok := err.(*textproto.Error); ok && e.Code != 0 {
			setProtocolMetrics(StringMismatch, operation, code, fields, tags)
		} else {
			setProtocolMetrics(ReadFailed, operation, code, fields, tags)
		}
		return false
	}
	setProtocolMetrics(Success, operation, code, fields, tags)
	return true
}

// sends the smtp commands to the server
// the data command expects an additional input to signal its end
func WriteCmd(c net.Conn, m string) {
	c.Write([]byte(m + "\r\n"))
}

// SMTPGather will execute the full smtp session.
// It will return a map[string]interface{} for fields and a map[string]string for tags
func (config *Smtp) SMTPGather() (tags map[string]string, fields map[string]interface{}) {
	// Prepare returns
	tags = make(map[string]string)
	fields = make(map[string]interface{})
	// Start Timer
	start := time.Now()
	// Connecting
	conn, err := net.DialTimeout("tcp", config.Address, config.Timeout.Duration)
	// Prepare reader
	reader := bufio.NewReader(conn)
	tp := textproto.NewReader(reader)
	// Stop timer
	responseTime := time.Since(start).Seconds()
	fields["connect_time"] = responseTime
	// Handle connection error
	if err != nil {
		if e, ok := err.(net.Error); ok && e.Timeout() {
			setResult(Timeout, fields, tags)
		} else {
			setResult(ConnectionFailed, fields, tags)
		}
		return tags, fields
	}
	defer conn.Close()

	// Set the overall read timeout
	conn.SetDeadline(time.Now().Add(config.ReadTimeout.Duration))

	// Commands are only executed if the previous one was successful

	// verify client connected
	success := CheckResponse(tp, Connect, 220, fields, tags)

	// perform required commands
	if success && config.Ehlo != "" {
		success = performCommand(conn, tp, Ehlo, "EHLO "+config.Ehlo, 250, fields, tags)
	}
	if success && config.StartTls {
		success = performCommand(conn, tp, StartTls, "STARTTLS", 220, fields, tags)
		if success {
			// read tls config
			tlsConfig, err := config.ClientConfig.TLSConfig()
			if err != nil || tlsConfig == nil{
				//setResult(TlsConfigError, fields, tags)
				//earlyEndSmtpSession(timeoutChan, client)
				return
			}
			// upgrade connection to tls
			conn = tls.Client(conn, tlsConfig)
			// update reader to use new connection
			reader = bufio.NewReader(conn)
			tp = textproto.NewReader(reader)
		}
	}
	if success && config.From != "" {
		success = performCommand(conn, tp, MailFrom, "MAIL FROM:"+config.From, 250, fields, tags)
	}

	if success && config.To != "" {
		success = performCommand(conn, tp, RcptTo, "RCPT TO:"+config.To, 250, fields, tags)
	}
	if success && config.Body != "" {
		// First check the response from "DATA"
		success = performCommand(conn, tp, Data, "DATA", 354, fields, tags)
		if success {
			// then the response from the body
			success = performCommand(conn, tp, Body, config.Body+"\r\n.\r\n", 250, fields, tags)
		}
	}

	// always execute the quit command
	if success {
		performCommand(conn, tp, Quit, "QUIT", 221, fields, tags)
	}

	responseTime = time.Since(start).Seconds()
	fields["total_time"] = responseTime
	return tags, fields
}

func performCommand(conn net.Conn, tp *textproto.Reader, operation Operation, msg string, expectedCode int, fields map[string]interface{}, tags map[string]string) (success bool) {
	WriteCmd(conn, msg)
	success = CheckResponse(tp, operation, expectedCode, fields, tags)
	if !success && operation != Quit {
		// if the request failed still try to quit cleanly
		WriteCmd(conn, "QUIT")
	}
	return success
}


// Gather is called by telegraf when the plugin is executed on its interval.
// It will call SMTPGather to generate metrics and also fill an Accumulator that is supplied.
func (smtp *Smtp) Gather(acc telegraf.Accumulator) error {
	// Set default values
	if smtp.Timeout.Duration == 0 {
		smtp.Timeout.Duration = time.Second
	}
	if smtp.ReadTimeout.Duration == 0 {
		smtp.ReadTimeout.Duration = time.Second * 10
	}
	// Prepare host and port
	host, port, err := net.SplitHostPort(smtp.Address)
	if err != nil {
		return err
	}
	if host == "" {
		smtp.Address = "localhost:" + port
	}
	if port == "" {
		return errors.New("Bad port")
	}
	// Prepare data
	tags := map[string]string{"server": host, "port": port}
	var fields map[string]interface{}
	var returnTags map[string]string
	// Gather data
	returnTags, fields = smtp.SMTPGather()
	// Merge the tags
	for k, v := range returnTags {
		tags[k] = v
	}
	// Add metrics
	acc.AddFields("smtp", fields, tags)
	return nil
}

func setProtocolMetrics(result ResultType, operation Operation, foundCode int, fields map[string]interface{}, tags map[string]string) {
	setResult(result, fields, tags)
	if foundCode != 0 {
		fields[string(operation)+"_code"] = foundCode
	}
}

func setResult(result ResultType, fields map[string]interface{}, tags map[string]string) {
	var tag string
	switch result {
	case Success:
		tag = "success"
	case Timeout:
		tag = "timeout"
	case ConnectionFailed:
		tag = "connection_failed"
	case ReadFailed:
		tag = "read_failed"
	case StringMismatch:
		tag = "string_mismatch"
	}

	fields["result_code"] = uint64(result)
	tags["result"] = tag
}

func init() {
	inputs.Add("smtp", func() telegraf.Input {
		return &Smtp{}
	})
}
