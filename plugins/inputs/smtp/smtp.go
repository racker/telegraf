package smtp

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/smtp"
	"net/textproto"
	"time"

	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/internal"
	internaltls "github.com/influxdata/telegraf/internal/tls"
	"github.com/influxdata/telegraf/plugins/inputs"
	"github.com/influxdata/wlog"
)

type ResultType uint64
type Operation string

const (
	Success ResultType = iota
	Timeout
	ConnectionFailed
	ReadFailed
	StringMismatch
	TlsConfigError
)

const (
	Connect  Operation = "connect"
	Ehlo               = "ehlo"
	StartTls           = "starttls"
	MailFrom           = "from"
	RcptTo             = "to"
	Data               = "data"
	Body               = "body"
	Quit               = "quit"
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
	StartTls    bool

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
  # timeout = "1s"

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

  ## Optional whether to issue "starttls" command
  # starttls = false

  ## Optional TLS Config
  # tls_ca = "/etc/telegraf/ca.pem"
  # tls_cert = "/etc/telegraf/cert.pem"
  # tls_key = "/etc/telegraf/key.pem"
  ## Use TLS but skip chain & host verification
  # insecure_skip_verify = true
`

// SampleConfig will return a complete configuration example with details about each field.
func (*Smtp) SampleConfig() string {
	return sampleConfig
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
	logMsg("Dialing tcp connection")
	conn, err := net.DialTimeout("tcp", config.Address, config.Timeout.Duration)
	if err != nil {
		setErrorMetrics(Connect, err, fields, tags)
		return tags, fields
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(config.ReadTimeout.Duration))
	// Prepare client
	host, _, _ := net.SplitHostPort(config.Address)
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		setErrorMetrics(Connect, err, fields, tags)
		return tags, fields
	}
	// Stop timer
	responseTime := time.Since(start).Seconds()
	fields["connect_time"] = responseTime
	setResponseCodeMetric(Connect, 220, fields, tags)
	// Handle connection error

	// Perform required commands
	// Commands are only executed if the previous one was successful
	var success bool = true

	if config.Ehlo != "" {
		if err := client.Hello(config.Ehlo); err != nil {
			setErrorMetrics(Ehlo, err, fields, tags)
			success = false
		} else {
			setResponseCodeMetric(Ehlo, 250, fields, tags)
		}
	}
	if success && config.StartTls {
		// read tls config
		tlsConfig, err := config.ClientConfig.TLSConfig()
		if err != nil || tlsConfig == nil {
			// update failure status
			setResult(TlsConfigError, fields, tags)
			success = false
		} else {
			if err := client.StartTLS(tlsConfig); err != nil {
				setErrorMetrics(StartTls, err, fields, tags)
				success = false
			} else {
				setResponseCodeMetric(StartTls, 220, fields, tags)
			}
		}
	}

	if success && config.From != "" {
		if err := client.Mail(config.From); err != nil {
			setErrorMetrics(MailFrom, err, fields, tags)
			success = false
		} else {
			setResponseCodeMetric(MailFrom, 250, fields, tags)
		}
	}

	if success && config.To != "" {
		if err := client.Rcpt(config.To); err != nil {
			setErrorMetrics(RcptTo, err, fields, tags)
			success = false
		} else {
			setResponseCodeMetric(RcptTo, 250, fields, tags)
		}
	}
	if success && config.Body != "" {
		w, err := client.Data()
		if err != nil {
			setErrorMetrics(Data, err, fields, tags)
			success = false
		}
		if success {
			setResponseCodeMetric(Data, 354, fields, tags)

			_, err1 := w.Write([]byte(config.Body))
			err2 := w.Close()
			if err1 != nil {
				setErrorMetrics(Body, err, fields, tags)
				success = false
			} else if err2 != nil {
				setErrorMetrics(Body, err2, fields, tags)
				success = false
			} else {
				setResponseCodeMetric(Body, 250, fields, tags)
			}
		}
	}

	// always execute the quit command
	if success {
		if err := client.Quit(); err != nil {
			setErrorMetrics(Quit, err, fields, tags)
			success = false
		} else {
			setResponseCodeMetric(Quit, 221, fields, tags)
		}
	} else {
		// attempt to cleanly close the connection but don't store extra metrics
		client.Quit()
	}

	if success {
		// set the final success result if everything went well
		setResult(Success, fields, tags)
	}
	responseTime = time.Since(start).Seconds()
	fields["total_time"] = responseTime
	return tags, fields
}

func setErrorMetrics(operation Operation, err error, fields map[string]interface{}, tags map[string]string) {
	var result ResultType
	if err != nil {
		if e, ok := err.(net.Error); ok && e.Timeout() {
			logMsg(fmt.Sprintf("Timed out when performing '%s' operation", string(operation)))
			result = Timeout
		} else if operation == Connect {
			logMsg(fmt.Sprintf("Failed to connect to server"))
			result = ConnectionFailed
		} else if e, ok := err.(*textproto.Error); ok && e.Code != 0 {
			logMsg(fmt.Sprintf("Received error response from '%s' operation: %d %s",
				string(operation), e.Code, e.Msg))

			fields[string(operation)+"_code"] = e.Code
			result = StringMismatch
		} else {
			logMsg(fmt.Sprintf("Read failed when performing %s operation", string(operation)))
			result = ReadFailed
		}
	}
	setResult(result, fields, tags)
}

func setResponseCodeMetric(operation Operation, expectedCode int, fields map[string]interface{}, tags map[string]string) {
	logMsg(fmt.Sprintf("Received expected response from '%s' operation", string(operation)))
	fields[string(operation)+"_code"] = expectedCode
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
	case TlsConfigError:
		tag = "tls_config_error"
	}

	fields["result_code"] = uint64(result)
	tags["result"] = tag
}

func logMsg(msg string) {
	if wlog.LogLevel() == wlog.DEBUG {
		log.Println("smtp: " + msg)
	}
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

func init() {
	inputs.Add("smtp", func() telegraf.Input {
		return &Smtp{}
	})
}
