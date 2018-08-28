[![Build Status](https://travis-ci.org/gcinterceptor/gci-proxy.svg?branch=master)](https://travis-ci.org/gcinterceptor/gci-proxy) [![Coverage Status](https://codecov.io/gh/gcinterceptor/gci-proxy/branch/master/graph/badge.svg)](https://codecov.io/gh/gcinterceptor/gci-proxy/branch/master/graph/badge.svg) [![Go Report Card](https://goreportcard.com/badge/github.com/gcinterceptor/gci-proxy)](https://goreportcard.com/report/github.com/gcinterceptor/gci-proxy) [![GoDoc](https://godoc.org/github.com/gcinterceptor/gci-proxy?status.svg)](https://godoc.org/github.com/gcinterceptor/gci-proxy)

# GCI-Proxy

> Disclaimer: a lot in flux

To help cloud developers to deal with the impact of non-deterministic garbage collection interventions, we implemented an easy-to-use mechanism called **Garbage Collection Control Interceptor (GCI)**. Instead of attempting to minimize the negative impact of garbage collector interventions, GCI controls the garbage collector and sheds the incoming load during collections, which avoids CPU competition and stop-the-world pauses while processing requests.

GCI has two main parts: i) the GCI-Proxy -- a multiplatform, runtime-agnostic HTTP intermediary responsible for controlling the garbage collector and shedding the load when necessary -- and the ii) the Request Processor(RP), a thin layer which runs within the service and is usually implemented as a framework middleware. The latter is responsible for checking the heap allocation and performing a garbage collection.


## Running GCI Proxy

```bash
./gci-proxy --port 3000 --url http://localhost:8080 --ygen=67108864 --tgen=6710886 --endpoint="/gci"
```

Where the flags:

* --ygen: size in bytes of the young generation, e.g. 67108864 (64MB)
* --tgen: size in bytes of the tenured generation, e.g. 67108864 (64MB)
* --port: port which the gci-proxy will listen, e.g. 3000
* --url: URL of the server being proxied, e.g. --url http://localhost:8080
* --endpoint: (optional) custom HTTP endpoint to send GCI-specific commands, e.g. --endpoint=/__gci


## GCI Proxy Protocol

The GCI-Proxy communicates to RPs through a simple protocol. The presence of the `gci` request header indicates that the RP must handle them, its absence means that RP should trigger the usual request processing chain. This header can assume three values:

* `ch` (check heap allocation): it is a blocking HTTP call, which expects the heap allocation in bytes as a response. For generational runtimes (e.g., Java and Ruby), the usage of each generation must be separated by `|` (pipe symbol), for example, 157810688|78905344 means that the young generation is using 128MB and tenured generation is using 64MB.
* `gen1` (collect gen1 garbage): it is a blocking HTTP call, which expects the cleanup of generation 1 (e.g., young) garbage. For generational runtimes, it usually represents a minor collection. 
* `gen2` (collect gen2 garbage): it is a blocking HTTP call, which expects the cleanup of generation 2 (e.g., tenured) garbage. For generational runtimes, it usually represents a major/full collection.

Example of RPs:

* Ruby
     * [Rack Middleware](https://github.com/gcinterceptor/gci-ruby/blob/master/lib/gci.rb)
* Go
     * [net/http Middleware](https://github.com/gcinterceptor/gci-go/blob/master/httphandler/handler.go)
     
Want to bring GCI to your language or framework, please get in touch!
