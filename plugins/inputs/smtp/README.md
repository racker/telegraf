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
> smtp,host=myhostname,port=25,result=success,server=localhost body_code=250i,connect_code=220i,connect_time=0.003485546,data_code=354i,ehlo_code=250i,from_code=250i,quit_code=221i,result_code=0i,starttls_code=220i,to_code=250i,total_time=0.054421634 1582754343000000000
```

When telegraf is running in debug mode the plugin will output some more details before the metrics:

```
2020-02-26T21:58:28Z I! smtp: Received expected response from 'connect' operation
2020-02-26T21:58:28Z I! smtp: Received expected response from 'ehlo' operation
2020-02-26T21:58:28Z I! smtp: Received expected response from 'starttls' operation
2020-02-26T21:58:28Z I! smtp: Received error response from 'to' operation: 503 5.5.1 Error: need MAIL command
> smtp,host=myhostname,port=25,result=string_mismatch,server=localhost connect_code=220i,connect_time=0.018595031,ehlo_code=250i,result_code=4i,starttls_code=220i,to_code=503i,total_time=0.06953827 1582754308000000000
```