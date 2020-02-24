package smtp
/*
import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	internaltls "github.com/influxdata/telegraf/internal/tls"
	"net"
	"net/textproto"
	"strings"
	"time"

	"github.com/imdario/mergo"
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
	TlsError
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

func upgradeToTls(config *Smtp, conn net.Conn, fields map[string]interface{}, tags map[string]string) error {
	// get cert info and upgrade the connection to tls
	tlsConfig, err := config.ClientConfig.TLSConfig()
	if err != nil {
		return err
	}
	if tlsConfig == nil {
		return errors.New("empty tls config")
	}

	tlsClient := tls.Client(conn, tlsConfig)
	tlserr := tlsClient.Handshake()
	if tlserr != nil {
		return tlserr
	}
	certs := tlsClient.ConnectionState().PeerCertificates
	// upgrade initial connection to tls
	conn = tlsClient

	setCertMetrics(config.Address, certs, fields, tags)
	return nil
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
	fmt.Println("connected")

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
	fmt.Println("Checking connect string")
	success := CheckResponse(tp, "connect", 220, fields, tags)
	if success && config.Ehlo != "" {
		fmt.Println("Checking ehlo string")
		WriteCmd(conn, "EHLO "+config.Ehlo)
		success = CheckResponse(tp, "ehlo", 250, fields, tags)
	}
	if success && config.StartTls {
		fmt.Println("Checking starttls string")
		WriteCmd(conn, "STARTTLS")
		success = CheckResponse(tp, "starttls", 220, fields, tags)
		if success {
			if err := upgradeToTls(config, conn, fields, tags); err != nil {
				success = false
				setResult(TlsError, fields, tags)
			}
		}
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

func setCertMetrics(source string, certs []*x509.Certificate, fields map[string]interface{}, tags map[string]string) {
	if len(certs) > 1 {
		return
	}
	cFields := getCertFields(certs[0], time.Now())
	cTags := getCertTags(certs[0], source)
	mergo.Merge(&fields, cFields)
	mergo.Merge(&tags, cTags)
}

// taken from x509_cert
func getCertFields(cert *x509.Certificate, now time.Time) map[string]interface{} {
	age := int(now.Sub(cert.NotBefore).Seconds())
	expiry := int(cert.NotAfter.Sub(now).Seconds())
	startdate := cert.NotBefore.Unix()
	enddate := cert.NotAfter.Unix()

	fields := map[string]interface{}{
		"age":       age,
		"expiry":    expiry,
		"startdate": startdate,
		"enddate":   enddate,
	}

	return fields
}

// taken from x509_cert
func getCertTags(cert *x509.Certificate, location string) map[string]string {
	tags := map[string]string{
		"source":               location,
		"common_name":          cert.Subject.CommonName,
		"serial_number":        cert.SerialNumber.Text(16),
		"signature_algorithm":  cert.SignatureAlgorithm.String(),
		"public_key_algorithm": cert.PublicKeyAlgorithm.String(),
	}

	if len(cert.Subject.Organization) > 0 {
		tags["organization"] = cert.Subject.Organization[0]
	}
	if len(cert.Subject.OrganizationalUnit) > 0 {
		tags["organizational_unit"] = cert.Subject.OrganizationalUnit[0]
	}
	if len(cert.Subject.Country) > 0 {
		tags["country"] = cert.Subject.Country[0]
	}
	if len(cert.Subject.Province) > 0 {
		tags["province"] = cert.Subject.Province[0]
	}
	if len(cert.Subject.Locality) > 0 {
		tags["locality"] = cert.Subject.Locality[0]
	}

	tags["issuer_common_name"] = cert.Issuer.CommonName
	tags["issuer_serial_number"] = cert.Issuer.SerialNumber

	san := append(cert.DNSNames, cert.EmailAddresses...)
	for _, ip := range cert.IPAddresses {
		san = append(san, ip.String())
	}
	for _, uri := range cert.URIs {
		san = append(san, uri.String())
	}
	tags["san"] = strings.Join(san, ",")

	return tags
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
*/