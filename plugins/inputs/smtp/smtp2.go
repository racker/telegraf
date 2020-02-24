package smtp

import (
	"errors"
	internaltls "github.com/influxdata/telegraf/internal/tls"
	"net"
	"net/smtp"
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
	EhloFailed
	StartTlsFailed
	MailFromFailed
	RcptToFailed
	DataFailed
	QuitFailed
	TlsConfigError
)

// Smtp struct
type Smtp struct {
	Address		string
	Timeout     internal.Duration
	ReadTimeout internal.Duration
	Ehlo        string
	From        string
	To          string
	Body        string
	StartTls	bool

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

  ## Optional whether to run starttls command
  # starttls = false
`

// SampleConfig will return a complete configuration example with details about each field.
func (*Smtp) SampleConfig() string {
	return sampleConfig
}

// SMTPGather will execute the full smtp session.
// It will return a map[string]interface{} for fields and a map[string]string for tags
func (config *Smtp) SMTPGather() (fields map[string]interface{}, tags map[string]string) {
	// Prepare returns
	tags = make(map[string]string)
	fields = make(map[string]interface{})

	host, _, _ := net.SplitHostPort(config.Address)

	// Start Timer
	start := time.Now()
	// Connecting
	conn, err := net.DialTimeout("tcp", config.Address, config.Timeout.Duration)
	//client, err := smtp.Dial(config.)
	if err != nil {
		setErrorMetrics(ConnectionFailed, "connect", nil, fields, tags)
		return endTimerAndReturnMetrics(start, fields, tags)
	} else {
		setSuccessMetrics(Success, "connect", 220, fields, tags)
	}
	client, err := smtp.NewClient(conn, host)
	defer client.Close()

	responseTime := time.Since(start).Seconds()
	fields["connect_time"] = responseTime

	// send ehlo command
	if config.Ehlo == "" {
		endSmtpSession(client, start, fields, tags)
	}
	if err := client.Hello(config.Ehlo); err != nil {
		setErrorMetrics(EhloFailed, "ehlo", err, fields, tags)
		return earlyEndSmtpSession(client, start, fields, tags)
	} else {
		setSuccessMetrics(Success, "ehlo", 250, fields, tags)
	}

	// configure tls
	if config.StartTls {
		tlsConfig, err := config.ClientConfig.TLSConfig()
		if err != nil || tlsConfig == nil{
			setResult(TlsConfigError, fields, tags)
			return earlyEndSmtpSession(client, start, fields, tags)
		}

		if err := client.StartTLS(tlsConfig); err != nil {
			setErrorMetrics(StartTlsFailed, "starttls", err, fields, tags)
			return earlyEndSmtpSession(client, start, fields, tags)
		} else {
			setSuccessMetrics(Success, "starttls", 220, fields, tags)
		}
	}

	// send mailfrom command
	if config.From == "" {
		endSmtpSession(client, start, fields, tags)
	}
	if err := client.Mail(config.From); err != nil {
		setErrorMetrics(MailFromFailed, "from", err, fields, tags)
		return earlyEndSmtpSession(client, start, fields, tags)
	} else {
		setSuccessMetrics(Success, "from", 250, fields, tags)
	}

	// send rcptto command
	if config.To == "" {
		endSmtpSession(client, start, fields, tags)
	}
	if err := client.Rcpt(config.To); err != nil {
		setErrorMetrics(RcptToFailed, "to", err, fields, tags)
		return earlyEndSmtpSession(client, start, fields, tags)
	} else {
		setSuccessMetrics(Success, "to", 250, fields, tags)
	}

	// send data command and payload
	if config.Body == "" {
		endSmtpSession(client, start, fields, tags)
	}
	w, err := client.Data()
	if err != nil {
		setErrorMetrics(DataFailed, "data", err, fields, tags)
		return earlyEndSmtpSession(client, start, fields, tags)
	}
	_, err1 := w.Write([]byte(config.Body))
	err2 := w.Close()
	if err1 != nil {
		setErrorMetrics(DataFailed, "data", err, fields, tags)
		return earlyEndSmtpSession(client, start, fields, tags)
	} else if err2 != nil {
		setErrorMetrics(DataFailed, "data", err2, fields, tags)
		return earlyEndSmtpSession(client, start, fields, tags)
	} else {
		setSuccessMetrics(Success, "data", 250, fields, tags)
	}

	// send quit command
	return endSmtpSession(client, start, fields, tags)
}

// called when a command has failed to attempt to cleanly close the connection before shutting down
func earlyEndSmtpSession(client *smtp.Client, start time.Time, fields map[string]interface{}, tags map[string]string) (map[string]interface{}, map[string]string) {
	// try and quit the client even after a failure
	client.Quit()
	return endTimerAndReturnMetrics(start, fields, tags)
}

// called when all required commands were successful
func endSmtpSession(client *smtp.Client, start time.Time, fields map[string]interface{}, tags map[string]string) (map[string]interface{}, map[string]string) {
	if err := client.Quit(); err != nil {
		setErrorMetrics(QuitFailed, "quit", err, fields, tags)
	} else {
		setSuccessMetrics(Success, "quit", 221, fields, tags)
	}

	return endTimerAndReturnMetrics(start, fields, tags)
}

// adds the final response time metric and returns all collected fields & tags
func endTimerAndReturnMetrics(start time.Time, fields map[string]interface{}, tags map[string]string) (map[string]interface{}, map[string]string) {
	// Stop timer
	responseTime := time.Since(start).Seconds()
	fields["total_time"] = responseTime

	return fields, tags
}

func setSuccessMetrics(result ResultType, operation string, expectedCode int, fields map[string]interface{}, tags map[string]string) {
	setResult(result, fields, tags)
	fields[operation+"_code"] = expectedCode
}

func setErrorMetrics(result ResultType, operation string, err error, fields map[string]interface{}, tags map[string]string) {
	if err != nil {
		if e, ok := err.(net.Error); ok && e.Timeout() {
			result = Timeout
		} else if e, ok := err.(*textproto.Error); ok && e.Code != 0 {
			fields[operation+"_code"] = e.Code
		} else {
			result = ReadFailed
		}
	}
	setResult(result, fields, tags)
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
	case EhloFailed:
		tag = "ehlo_failed"
	case MailFromFailed:
		tag = "from_failed"
	case RcptToFailed:
		tag = "to_failed"
	case StartTlsFailed:
		tag = "starttls_failed"
	case DataFailed:
		tag = "data_failed"
	case QuitFailed:
		tag = "quit_failed"
	case TlsConfigError:
		tag = "tls_config_error"
	}

	tags["result"] = tag
	fields["result_code"] = uint64(result)
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
	fields, returnTags = smtp.SMTPGather()
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
