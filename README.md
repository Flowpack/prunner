<img src="docs/prunner-logo-light.png" width="320" align="center">

---

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

prunner controls a set of *pipelines*, which are defined in *YAML* files (typically `pipelines.yml`).
The pipelines consist of *tasks*, which are executed as part of the pipeline. Each task has a `script`
which are the CLI commands executed when the task is run.

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
  env:
    MY_VAR: set some value for all tasks here
  do_something:
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

Note: If prunner is killed hard (e.g. SIGKILL) without SIGINT / SIGTERM, the child processes of running jobs will not be terminated.

### Graceful shutdown

Prunner will handle a SIGINT signal and perform a graceful shutdown and wait for all running jobs to be completed.
Sending a SIGTERM signal to prunner will cancel all running jobs (and interrupt / kill child processes).

### Reloading definitions and watching for changes

Prunner will reload pipeline definitions from disk when a SIGUSR1 signal is received (and if the definitions changed).
A watch mode that polls the files for changes can be enabled with the `--watch` flag. The poll interval is configurable
via `--poll-interval`.

Note: A running pipeline job and completed jobs are not changed when the definitions are reloaded.

### Persistent job state

## Development

### Requirements

* Go (>= 1.18)

### Running locally

```bash
go run ./cmd/prunner
```
> Note: for development a live reload wrapper like https://github.com/markbates/refresh is recommended.

The API should now be accessible at http://localhost:9009/. The log will contain an example JWT auth token that can be used for authentication in local development.

For interacting with the API, you need a JWT token which you can generate for developing using:

```bash
go run ./cmd/prunner debug
```

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
* prunner always enables authentication via JWT
* An application that wants to embed prunner should read the shared secret and generate a JWT auth token for accessing the API by
  doing internal HTTP requests. This way custom policies can be implemented for ensuring access to prunner.

## License

GPL, because we are building on [taskctl](https://github.com/taskctl/taskctl) - see [LICENSE](LICENSE).
