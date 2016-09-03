gonlineviz
==========

gonlineviz is a dependency graph visualization tool. It's basically [goviz](https://github.com/hirokidaichi/goviz) but available on a HTTP endpoint.

installation
===========

    go get github.com/vrischmann/gonlineviz

usage
=====

    gonlineviz [-listen <listen address>] [-goroot <goroot>]

The default values are:

  * `LISTEN_ADDRESS` environment variable, or `localhost:3245`
  * `GOROOT` environment variable, or `/usr/local/go`

The `GOPATH` environment variable needs to be set.

Once running, simply points your browser to `http://localhost:3245/<package path>` where `package path` is an import path in the form `github.com/vrischmann/envconfig` or `golang.org/x/tools/vcs`

parameters
==========

There are two parameters:

  * `leaf`, `true` or `false`. If true, draws the leafs too.
  * `reversed`, a package path. If set, graph starting from this package.
  * `depth`, an int. If set, limit the graph depth.

caveats
=======

For the graph generation to work, the packages must be present on disk AND compiled. This means this tool uses `go get` for each package it doesn't already have a recent version of.

If you try to graph a big app or library, it will take quite some time to compile depending on your machine.

live demo
=========

There's a live demo [here](https://vrischmann.me/goviz).

Some basic examples:

  * [gocql](https://vrischmann.me/goviz/github.com/gocql/gocql)
  * [envconfig](https://vrischmann.me/goviz/github.com/vrischmann/envconfig)

Some examples using `leaf=true`

  * [sarama](https://vrischmann.me/goviz/github.com/Shopify/sarama?leaf=true)
  * [docker](https://vrischmann.me/goviz/github.com/docker/docker?depth=5&leaf=true)

Some examples using `reversed`

  * focusing on `golang.org/x/net/context` [docker](https://vrischmann.me/goviz/github.com/docker/docker?reversed=golang.org/x/net/context)

NOTE: the live demo is super slow to compile, and rate limited. Don't break it.

thanks
======

Thanks to [goviz](https://github.com/hirokidaichi/goviz) for the graph generation code.

license
=======

MIT licensed, see the LICENSE file.
