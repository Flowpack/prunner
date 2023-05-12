<img src="docs/prunner-logo-light.png" alt="prunner" width="320" align="center">

---
[![GoDoc](https://godoc.org/github.com/Flowpack/prunner?status.svg)](https://godoc.org/github.com/Flowpack/prunner)
[![Build Status](https://github.com/Flowpack/prunner/actions/workflows/test.yml/badge.svg)](https://github.com/Flowpack/prunner/actions/workflows/test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/Flowpack/prunner)](https://goreportcard.com/report/github.com/Flowpack/prunner)
[![codecov](https://codecov.io/gh/Flowpack/prunner/branch/main/graph/badge.svg?token=RTYBJ3ACPT)](https://codecov.io/gh/Flowpack/prunner)

**Prunner is an embeddable task / pipeline runner with an HTTP API.**

- It is easy to embed in your own projects: just a single binary, no database or other services needed.
- Good for orchestrating long-running jobs with multiple steps; so if you think "I need a CI pipeline" but within your project, this is for you.
- The pipeline definition is done in a `pipelines.yml` file and is static.
- To start a new job, you use an authenticated HTTP API (see our [API docs](https://bump.sh/doc/prunner)).
- Every task inside the pipeline is a script run on the command line.
- Tasks can have dependencies; so together, they form a graph (DAG).
- It supports runtime variables for pipelines.
- It supports configurable parallelism, also with a "wait-list" if the parallelism is exceeded.
- It has a persistent storage of jobs and their outputs.

This is NOT a fully featured CI pipeline solution.

<!-- TOC -->
  * [Badges](#badges)
  * [Components](#components)
    * [prunner (this repository)](#prunner--this-repository-)
    * [prunner-ui](#prunner-ui)
    * [Flowpack.Prunner](#flowpackprunner)
  * [User guide](#user-guide)
    * [Main concepts](#main-concepts)
    * [A simple pipeline](#a-simple-pipeline)
    * [Task dependencies](#task-dependencies)
    * [Job variables](#job-variables)
    * [Environment variables](#environment-variables)
      * [Dotenv files](#dotenv-files)
    * [Limiting concurrency](#limiting-concurrency)
    * [The wait list](#the-wait-list)
    * [Debounce jobs with a start delay](#debounce-jobs-with-a-start-delay)
    * [Disabling fail-fast behavior](#disabling-fail-fast-behavior)
    * [Configuring retention period](#configuring-retention-period)
    * [Handling of child processes](#handling-of-child-processes)
    * [Graceful shutdown](#graceful-shutdown)
    * [Reloading definitions and watching for changes](#reloading-definitions-and-watching-for-changes)
    * [Persistent job state](#persistent-job-state)
  * [Running prunner](#running-prunner)
    * [CLI Reference](#cli-reference)
    * [Docker](#docker)
  * [Development](#development)
    * [Requirements](#requirements)
    * [Running locally](#running-locally)
    * [IDE Setup (IntelliJ/GoLand)](#ide-setup--intellijgoland-)
    * [Building for different operating systems.](#building-for-different-operating-systems)
    * [Running Tests](#running-tests)
    * [Memory Leak Debugging](#memory-leak-debugging)
    * [Generate OpenAPI (Swagger) spec](#generate-openapi--swagger--spec)
    * [Releasing](#releasing)
  * [Security concept](#security-concept)
  * [License](#license)
<!-- TOC -->

## Badges

[![Release](https://img.shields.io/github/release/Flowpack/prunner.svg?style=for-the-badge)](https://github.com/Flowpack/prunner/releases/latest)
[![Software License](https://img.shields.io/badge/license-GPLv3-brightgreen.svg?style=for-the-badge)](/LICENSE)
[![Build status](https://img.shields.io/github/workflow/status/Flowpack/prunner/run%20tests?style=for-the-badge)](https://github.com/Flowpack/prunner/actions?workflow=run%20tests)
[![Coverage](https://img.shields.io/coveralls/github/Flowpack/prunner.svg?style=for-the-badge)](https://coveralls.io/github/Flowpack/prunner)

## Components

### [prunner](https://github.com/Flowpack/prunner) (this repository)

A single process, written in go, that provides the REST API, pipeline runner and persistence.
It needs to be started in the background for integration into other applications.

### [prunner-ui](https://github.com/Flowpack/prunner-ui)

A minimalistic React UI to start and view pipelines, job and task details.

### [Flowpack.Prunner](https://github.com/Flowpack/Flowpack.Prunner)

A Neos/Flow PHP package providing a backend module embedding prunner-ui and a PHP API for interacting with the prunner Rest API.

## User guide

### Main concepts

Prunner controls a set of *pipelines*, which are defined in *YAML* files (typically `pipelines.yml`).
The pipelines consist of *tasks*, which are executed as part of the pipeline. Each task has a `script`
which are the commands executed when the task is run. A pipeline can be scheduled as a *job* via the REST API.
Depending on the definition it is started immediately or put on a wait list.

### A simple pipeline

Tasks are run in-parallel by default. In the example below, if the pipeline `do_something`
is started, the two tasks `do_foo` and `do_bar` run in parallel to each other:

```yaml
pipelines:
  do_something:
    tasks:
      do_foo:
        script:
          - pwd
      do_bar:
        script:
          - ls
```


### Task dependencies

In case you need to ensure certain steps are executed in-order, you can use
**task dependencies** to order tasks using the `depends_on` key:

```yaml
pipelines:
  do_something:
    tasks:
      do_foo:
        script:
          - pwd
      do_bar:
        script:
          - ls
        # here, we ensure that do_bar runs AFTER do_foo.
        depends_on:
          - do_foo
```

**It is not possible to pass information from one task to the next one within prunner.**
This is an intended limitation to keep complexity low; so we do not plan to support "artifacts"
or anything like this.

In case you need to store information from one task to the next, it is recommended that you
do this outside prunner, and pass in a job argument with an identifier to every task
(explained in the next section).


### Job variables

When starting a job, (i.e. `do_something` in the example below), you can send additional
**variables** as JSON. The script is passed through the Go [text/template](https://pkg.go.dev/text/template)
templating language, where you can access the variables. This way, you can pass the variable
contents to your scripts.

```yaml
pipelines:
  do_something:
    tasks:
      do_foo:
        script:
          - pwd {{ .myVariable }}
      do_bar:
        script:
          - echo {{ .myVariable }}
```

> Note that these variables are _not environment variables (env vars)_ and are evaluated via the template engine before the shell invokes the script commands.

### Environment variables

Environment variables are handled in the following places:

1. **Process level** Prunner will forward the environment variables of the `prunner` process (including dotenv overrides) to commands executed by tasks
2. **Pipeline level** Environment variables can be set/overridden in a pipeline definition (overrides process level)
3. **Task level** Environment variables can be set/overridden in a task definition (overrides pipeline level)


```yaml
pipelines:
  do_something:
    env:
      MY_VAR: set some value for all tasks here
    tasks:
      do_foo:
        script:
          # output: set some value for all tasks here\n
          - echo $MY_VAR
      do_bar:
        env:
          MY_VAR: override it for this task
        script:
          # output: override it for this task\n
          - echo $MY_VAR
```

#### Dotenv files

Prunner will override the process environment from files `.env` and `.env.local` by default.
The files are configurable via the `env-files` flag.

### Limiting concurrency

Certain pipelines, like deployment pipelines, usually should only run only once, and never be started
concurrently. Prunner supports this via a configurable *concurrency*:

```yaml
pipelines:
  do_something:
    concurrency: 1
    tasks: # as usual
```

*Concurrency specifies how often the pipeline can run concurrently; NOT whether individual
tasks in the pipeline run concurrently.*

Now, when the concurrency limit is reached and you schedule the pipeline again while it is running,
**the job is queued** to be worked on later - it is added to the wait list by default.

### The wait list

By default, if you limit concurrency, and the limit is exceeded, further jobs are added to the
waitlist of the pipeline.

However, you have some options to configure this as well:

The waitlist can have a maximum size, denoted by `queue_limit`:

```yaml
pipelines:
  do_something:
    queue_limit: 1
    concurrency: 1
    tasks: # as usual
```

To deactivate the queuing altogether, set `queue_limit: 0`.

Now, if the queue is limited, an error occurs when it is full and you try to add a new job.

Alternatively, you can also set `queue_strategy: replace` to replace the last job in the
queue by the newly added one:

```yaml
pipelines:
  do_something:
    queue_limit: 1
    queue_strategy: replace
    concurrency: 1
    tasks: # as usual
```

So the example above means:

- at most one pipeline of do_something runs at any given time (`concurrency: 1`)
- in case a pipeline is running and a new job is added, this is added to the queue.
- when *another* job is added, it **replaces** the previously added job on the waitlist.

This is especially helpful for stuff like incremental content rendering, when you need
to ensure that the system converges to the last known state.

### Debounce jobs with a start delay

Sometimes it is desirable to delay the actual start of a job and wait until some time has passed and no other start of
the same pipeline was triggered. This is especially useful with `queue_strategy: replace` where this can act as a
debounce of events (e.g. a user in an application performs some actions and a pipeline run is triggered for each action).

The delay can be configured on the pipeline level with the `start_delay` property. The value is given as duration
in form of a zero or positive decimal value with a time unit ("ms", "s", "m", "h" are supported):

```yaml
pipelines:
  do_something:
    queue_limit: 1
    queue_strategy: replace
    concurrency: 1
    # Queues a run of the job and only starts it after 10 seconds have passed (if no other run was triggered which replaced the queued job)
    start_delay: 10s
    tasks: # as usual
```


### Disabling fail-fast behavior

By default, if a task in a pipeline fails, all other concurrently running tasks are directly aborted.
Sometimes this is not desirable, e.g. if certain deployment tasks should continue running if already started.

For now, this is not configurable on a per-task basis, but only on a per-pipeline basis, by setting
`continue_running_tasks_after_failure` to `true`:

```yaml
pipelines:
  do_something:
    continue_running_tasks_after_failure: true
    tasks: # as usual
```


### Configuring retention period

By default, we never delete any runs. For many projects, it is useful to configure this to keep the
consumed disk space under control. This can be done on a per-pipeline level; using one of the two configuration
settings `retention_period` (decimal with time unit as in `start_delay`) and `retention_count`.

As an example, let's configure we only are interested on the last 10 pipeline runs:

```yaml
pipelines:
  do_something:
    retention_count: 10
    tasks: # as usual
```

Alternatively, we can delete the data after two days:

```yaml
pipelines:
  do_something:
    retention_period: 48h
    tasks: # as usual
```

You can also combine the two options. Then, deletion occurs with whatever comes first.

If a pipeline does not exist at all anymore (i.e. if you renamed `do_something` to `another_name` above),
its persisted logs and task data is removed automatically on saving to disk.

### Handling of child processes

Prunner starts child processes with `setpgid` to use a new process group for each task of a pipeline job.
This means that if a job is cancelled, all child processes are killed - even if they were run by a shell script.

> Note: If prunner is killed hard (e.g. SIGKILL) without SIGINT / SIGTERM, the child processes of running jobs will not be terminated.

> Windows support: Process groups are not used, since there is not `setpgid` on Windows.

### Graceful shutdown

Prunner will handle a SIGINT signal and perform a graceful shutdown and wait for all running jobs to be completed.
Sending a SIGTERM signal to prunner will cancel all running jobs (and interrupt / kill child processes).

### Reloading definitions and watching for changes

Prunner will reload pipeline definitions from disk when a SIGUSR1 signal is received (and if the definitions changed).

> Windows support: There is no SIGUSR1 signal on Windows. You have to use watch mode to reload definitions.

A watch mode that polls the files for changes can be enabled with the `--watch` flag. The poll interval is configurable
via `--poll-interval`.

> Note: Only newly scheduled jobs use the updated definitions. Running jobs and jobs that are queued for execution
continue to use the old definition.

### Persistent job state

The state of pipeline jobs is persisted to disk in the `.prunner` directory regularly.
The directory can be configured via the `--data` flag.
Logs for script output (STDERR and STDOUT) of tasks are stored in the `[data]/logs` directory.

## Running prunner

Since prunner is only a single binary, it can be easily deployed and run in a variety of environments.
It is designed to run in the foreground and output logs to STDERR and generally follows the rules of a [twelve-factor](https://12factor.net) app.

### CLI Reference

```
NAME:
   prunner - Pipeline runner

USAGE:
   prunner [global options] command [command options] [arguments...]

COMMANDS:
   debug    Get authorization information for debugging
   version  Print the current version
   help, h  Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --verbose, -v          Enable verbose log output (default: false) [$PRUNNER_VERBOSE]
   --disable-ansi         Force disable ANSI log output and output log in logfmt format (default: false) [$PRUNNER_DISABLE_ANSI]
   --config value         Dynamic config filename (will be created on first run if jwt-secret is not set) (default: ".prunner.yml") [$PRUNNER_CONFIG]
   --jwt-secret value     Pre-generated shared secret for JWT authentication (at least 16 characters) [$PRUNNER_JWT_SECRET]
   --data value           Base directory to use for storing data (metadata and job outputs) (default: ".prunner") [$PRUNNER_DATA]
   --pattern value        Search pattern (glob) for pipeline configuration scan (default: "**/pipelines.{yml,yaml}") [$PRUNNER_PATTERN]
   --path value           Base directory to use for pipeline configuration scan (default: ".") [$PRUNNER_PATH]
   --address value        Listen address for HTTP API (default: "localhost:9009") [$PRUNNER_ADDRESS]
   --env-files value      Filenames with environment variables to load (dotenv style), will override existing env vars, set empty to skip loading (default: ".env", ".env.local")  (accepts multiple inputs) [$PRUNNER_ENV_FILES]
   --watch                Watch for pipeline configuration changes and reload them (default: false) [$PRUNNER_WATCH]
   --poll-interval value  Poll interval for pipeline configuration changes (if watch is enabled) (default: 30s) [$PRUNNER_POLL_INTERVAL]
   --help, -h             show help (default: false)
```

> Note: Options can be passed as command line flags or as environment variables.

### Docker

Prunner can be started inside a container. There are a few things to consider:

* All tasks are executed in the container where prunner is running. Make sure to use a base image with the necessary dependencies.
* For graceful shutdown: Use `STOPSIGNAL SIGINT` in a custom docker image that runs prunner or start the docker container via `--stop-signal SIGINT`
* The data directory (defaults to `.prunner`) should be placed in a volume to persist restarts
* The dynamic config (defaults to `.prunner.yml`) should be placed in a volume or mounted from an existing file to allow
  clients to generate correct JWT tokens based on the secret.
* Alternatively a pre-generated secret can be passed via the `PRUNNER_JWT_SECRET` env var
  and shared with applications accessing the API.
* The `--address` flag should be set to listen on all interfaces (.e.g. `:9009`) or a specific network address.
  This allows to access the API from outside the container.

## Development

### Requirements

* Go (>= 1.18)

### Running locally

```bash
go run ./cmd/prunner --path examples
```
> Note: for development a live reload wrapper like https://github.com/networkteam/refresh is recommended.

The API should now be accessible at http://localhost:9009/. The log will contain an example JWT auth token that can be used for authentication in local development.

For interacting with the API, you need a JWT token which you can generate for developing using:

```bash
go run ./cmd/prunner debug
```
### IDE Setup (IntelliJ/GoLand)

- Please install [Go](https://plugins.jetbrains.com/plugin/9568-go) Plugin in IntelliJ.
- In the Settings of IntelliJ: Activate `Languages & Frameworks -> Go -> Go Modules` - `Enable Go Modules Integration`
- Open a Go File. At the top of the screen the following message appears: `GOROOT is not defined` -> `Setup GOROOT` -> `/usr/local/opt/go/libexec`
- If autocompletion / syntax check shows lots of things red, try the following two steps:
  - restart the IDE
  - if this does not help, `File -> Invalidate Caches`

- Run / Debug in IDE:
    - `Run -> Edit Configurations`
    - `Add new Run Configuration` -> `Go Build`
    - Files: `.../cmd/prunner/main.go`
    - Working Directory: `.../`

- Tests:
    - `Run -> Edit Configurations`
    - `Add new Run Configuration` -> `Go Test`
    - Test Kind: Package (otherwise you cannot set breakpoints)

### Building for different operating systems.

Using the standard `GOOS` environment variable, you can build for different operating systems. This is helpful when you
want to use prunner inside a Docker container, but are developing on macOS. For this, a compile step like the following is useful:

```bash
# after building, copy the executable inside the docker container; so it can be directly used.
GOOS=linux go build ./cmd/prunner -o bin/prunner && docker cp bin/prunner my_container:/app/prunner
```

### Running Tests

To run all tests, use:

```bash
go test ./...

# to show test output, run with verbose flag:
go test ./... -v

# to run a single test, use -run:
go test ./... -v -run TestServer_HugeOutput
```

As linter, we use golangci-lint. See [this page for platform-specific installation instructions](https://golangci-lint.run/usage/install/#local-installation).
Then, to run the linter, use:

```bash
golangci-lint run
```

### Memory Leak Debugging

to find memory leaks, you can run `prunner` in the following way:

```bash
# start prunner in profiling mode with the config from test/memory_leak_debugging/pipelines.yml
./dev.sh memory-leak-start

# run a pipeline which creates many MB of log output (possibly multiple times)
./dev.sh start-pipeline memleak1

# analyze heap dump
./dev.sh analyze-heapdump
```

### Generate OpenAPI (Swagger) spec

An OpenAPI 2.0 spec is generated from the Go types and annotations in source code using the `go-swagger` tool (it is not
bundled in this module). See https://goswagger.io/install.html for installation instructions.

```bash
go generate ./server
```

### Releasing

Releases are done using goreleaser and GitHub Actions. Simply tag a new version using the `vX.Y.Z` naming convention,
and then all platforms are built automatically.

## Security concept

* The HTTP server only listens on localhost by default
* Prunner always enables authentication via JWT (HS256), a random shared secret is generated in the dynamic config file (`.prunner.yml` by default) if it does not exist
* An application that wants to embed prunner should read the shared secret (`jwt_secret`) and generate a JWT auth token for accessing the API
* The JWT secret can alternatively be passed via env var (`PRUNNER_JWT_SECRET`) (passing via flag is not recommended)
* The HTTP API of prunner should not be exposed directly to the outside, but requests should be forwarded by the application embedding prunner.
  This way custom policies can be implemented in the consumer app for ensuring/limiting access to prunner.

## License

GPL, because we are building on [taskctl](https://github.com/taskctl/taskctl) - see [LICENSE](LICENSE).
