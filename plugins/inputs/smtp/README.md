# SMTP Input Plugin

The input plugin can test a full SMTP session, reporting response times and codes.

With no optional parameters set the plugin will verify the connect and quit codes.  With each additional parameters the plugin will execute it's corresponding command while also reporting the response code seen.

Two response time metrics are returned.  One for the initial connection time and another for all operations to be completed.

### Configuration:

```toml
# Collect response time and codes for an SMTP session
[[inputs.smtp]]
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
```

### Metrics:

- smtp
  - tags:
    - server
    - port
    - result
  - fields:
    - connect_time (float, seconds)
    - total_time (float, seconds)
    - result_code (int, success = 0, timeout = 1, connection_failed = 2, read_failed = 3, string_mismatch = 4, tls_config_error = 5, command_failed = 6)
    - connect_code (int, if available)
    - ehlo_code (int, if available)
    - starttls_code (int, if available)
    - from_code (int, if available)
    - to_code (int, if available)
    - data_code (int, if available)
    - body_code (int, if available)
    - quit_code (int, if available)

### Example Output:

```
> smtp,host=myhostname,port=25,result=success,server=localhost body_code=250i,connect_code=220i,connect_time=0.003375849,data_code=354i,ehlo_code=250i,from_code=250i,quit_code=221i,result_code=0i,to_code=250i,total_time=0.01299625 1582748272000000000
```

When telegraf is running in debug mode the plugin will output some more details before the metrics:

```
2020-02-26T20:24:30Z I! Starting Telegraf 1.13.0-rc1
2020-02-26T20:24:30Z I! Using config file: /etc/telegraf/telegraf.conf
2020-02-26T20:24:30Z D! [agent] Initializing plugins
2020-02-26T20:24:30Z I! Dialing tcp connection
2020-02-26T20:24:30Z I! Received response from connect operation: 220 myhostname ESMTP Postfix (Ubuntu)
2020-02-26T20:24:30Z I! Executing operation: EHLO example.com
2020-02-26T20:24:30Z I! Received response from ehlo operation: 250 myhostname
PIPELINING
SIZE 10240000
VRFY
ETRN
STARTTLS
ENHANCEDSTATUSCODES
8BITMIME
DSN
SMTPUTF8
2020-02-26T20:24:30Z I! Executing operation: MAIL FROM:<me@example.com>
2020-02-26T20:24:30Z I! Received response from from operation: 250 2.1.0 Ok
2020-02-26T20:24:30Z I! Executing operation: RCPT TO:<you@example.com>
2020-02-26T20:24:30Z I! Received response from to operation: 250 2.1.5 Ok
2020-02-26T20:24:30Z I! Executing operation: DATA
2020-02-26T20:24:30Z I! Received response from data operation: 354 End data with <CR><LF>.<CR><LF>
2020-02-26T20:24:30Z I! Sending data payload: this is a test payload
2020-02-26T20:24:30Z I! Received response from body operation: 250 2.0.0 Ok: queued as 8D68A4269B
2020-02-26T20:24:30Z I! Executing operation: QUIT
2020-02-26T20:24:30Z I! Received response from quit operation: 221 2.0.0 Bye
> smtp,host=myhostname,port=25,result=success,server=localhost body_code=250i,connect_code=220i,connect_time=0.01893857,data_code=354i,ehlo_code=250i,from_code=250i,quit_code=221i,result_code=0i,to_code=250i,total_time=0.03669411 1582748671000000000
```