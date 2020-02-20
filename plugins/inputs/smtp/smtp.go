package smtp

import (
	"bufio"
	"errors"
	"net"
	"net/textproto"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/internal"
	"github.com/influxdata/telegraf/plugins/inputs"
)

type ResultType uint64

const (
	Success ResultType = iota
	Timeout
	ConnectionFailed
	ReadFailed
	StringMismatch
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
	Tls         bool
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

func CheckResponse(r *textproto.Reader, operation string, expectedCode int, fields map[string]interface{}, tags map[string]string) (success bool) {
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
	if m == "DATA" {
		c.Write([]byte(".\r\n"))
	}
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
	// Handle error
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
	success := CheckResponse(tp, "connect", 220, fields, tags)
	if success && config.Ehlo != "" {
		WriteCmd(conn, "EHLO "+config.Ehlo)
		success = CheckResponse(tp, "ehlo", 250, fields, tags)
	}
	if success && config.From != "" {
		WriteCmd(conn, "MAIL FROM:"+config.From)
		success = CheckResponse(tp, "from", 250, fields, tags)
	}
	if success && config.To != "" {
		WriteCmd(conn, "RCPT TO:"+config.To)
		success = CheckResponse(tp, "to", 250, fields, tags)
	}
	if success && config.Body != "" {
		WriteCmd(conn, "DATA\r\n"+config.Body)
		// First check the response from "DATA"
		success = CheckResponse(tp, "data", 354, fields, tags)
		if success {
			// then the response from the body
			success = CheckResponse(tp, "body", 250, fields, tags)
		}
	}

	// Send a quit whether the previous commands succeeded or not
	WriteCmd(conn, "QUIT")
	success = CheckResponse(tp, "quit", 221, fields, tags)

	responseTime = time.Since(start).Seconds()

	fields["total_time"] = responseTime
	return tags, fields
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

func setProtocolMetrics(result ResultType, operation string, foundCode int, fields map[string]interface{}, tags map[string]string) {
	setResult(result, fields, tags)
	fields[operation+"_code"] = foundCode
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

	tags["result"] = tag
	fields["result_code"] = uint64(result)
}

func init() {
	inputs.Add("smtp", func() telegraf.Input {
		return &Smtp{}
	})
}
