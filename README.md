# GCI-Proxy

> Disclaimer: a lot in flux

To help cloud developers to deal with the impact of non-deterministic garbage collection interventions, we implemented an easy-to-use mechanism called **Garbage Collection Control Interceptor (GCI)**. Instead of attempting to minimize the negative impact of garbage collector interventions, GCI controls the garbage collector and sheds the incoming load during collections, which avoids CPU competition and stop-the-world pauses while processing requests.

The GCI-Proxy is a multiplatform, runtime-agnostic HTTP intermediary responsible for controlling the garbage collector and shedding the load when necessary. The GCI-Proxy sits in front of the service instance and has a counterpart, i.e., the Request Processor(RP), which is usually implemented as a framework middleware. The latter is responsible for checking the heap allocation and performing a garbage collection.

The RPs must implement a straightforward protocol, which consists of checking the `gci` request header. The presence of the `gci` request header indicates that requests are specific to GCI protocol and should not be delivered to the service handling. The `gci` request header can assume three values:

* `ch` (check heap allocation): it is a blocking HTTP call, which expects the heap allocation in bytes as a response. For generational runtimes (e.g., Java and Ruby), the usage of each generation must be separated by `|` (pipe symbol), for example, 157810688|78905344 means that the young generation is using 128MB and tenured generation is using 64MB.
* `gen1` (collect gen1 garbage): it is a blocking HTTP call, which expects the cleanup of generation 1 (e.g., young) garbage. For generational runtimes, it usually represents a minor collection. 
* `gen2` (collect gen2 garbage): it is a blocking HTTP call, which expects the cleanup of generation 2 (e.g., tenured) garbage. For generational runtimes, it usually represents a major/full collection.

The absence of the `gci` header indicates that the usual request handling flow should take place.

Example of RPs:

* Ruby
     * [Rack Middleware](https://github.com/gcinterceptor/gci-ruby/blob/master/lib/gci.rb)
* Go
     * [net/http Middleware](https://github.com/gcinterceptor/gci-go/blob/master/httphandler/handler.go)
     
Want to bring GCI to your language or framework, please get in touch!
