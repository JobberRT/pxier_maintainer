# pxier_maintainer
`pxier_maintainer` is the maintainer for [Pxier](https://github.com/JobberRT/pxier), it will constantly fetch proxy and validate connection.

## Configuration
`pxier_base_url`: http:// + HOST url to `pxier`, WITHOUT ending `/`
`each_fetch_num`: how many proxies for each request
`max_concurrency`: how many goroutine will be started at the same time
`check_connection_url`: url for check proxy connection
`write_db`: database for writing, more detail on [Pxier](https://github.com/JobberRT/pxier)
`max_err`: how many error times can a proxy get before marked as `unusable` and then delete

## How to use
Recommend to use [Pxier](https://github.com/JobberRT/pxier) README's docker-compose file to deploy. Otherwise, you can compile and change the configuration and rename the `config.example.yaml` to `config.yaml`, then you can start the executable.