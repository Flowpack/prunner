# Pipeline Runner

**An embeddable task / pipeline runner with a HTTP API.**

- Good for orchestrating long-running jobs with multiple steps; so if you think "I need a CI pipeline" but within your project, this is for you.
- The pipeline definition is done in a `pipelines.yml` file and is static.
- To start a new job, you use an authenticated HTTP API (see our [API docs](https://bump.sh/doc/prunner)).
- Every task inside the pipeline is a script run on the command line.
- Tasks can have dependencies; so together, they form a graph (DAG)
- supports pipeline arguments
- supports configurable parallelism, also with a "wait-list" if the parallelism is exceeded.
- Persistent storage of jobs and their outputs

This is NOT a full CI pipeline solution.

**For a full introduction, see the [README of the prunner repo](https://github.com/Flowpack/prunner)**.

## Components

### [prunner](https://github.com/Flowpack/prunner) (this repository)

A single process, written in go, that provides the REST API, pipeline runner and persistence.
It needs to be started in the background for integration into other applications.

### [prunner-ui](https://github.com/Flowpack/prunner-ui)

A minimalistic React UI to start and view pipelines, jobs and task details.

### [Flowpack.Prunner](https://github.com/Flowpack/Flowpack.Prunner)

A Neos/Flow PHP package providing a backend module for the current pipeline state, and a PHP API.

## User Guide

### Main Concepts

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


### Task Dependencies

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
do this outside prunner, and pass in a Job Argument with an identifier to every task
(explained in the next section).


### Job Variables

When starting a job, (i.e. `do_something` in the example below), you can send additional
**Variables** as JSON. The script is passed through the [text/template](https://pkg.go.dev/text/template)
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

### Limiting Concurrency

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

### The Wait List

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


### Disabling Fail-Fast Behavior

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


### Configuring Retention Period

By default, we never delete any runs. For many projects, it is useful to configure this to keep the
consumed disk space under control. This can be done on a per-pipeline level; using one of the two configuration
settings `retention_period_hours` and `retention_count`:

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
    retention_period_hours: 48
    tasks: # as usual
```

You can also combine the two options. Then, deletion occurs with whatever comes first.

If a pipeline does not exist at all anymore (i.e. if you renamed `do_something` to `another_name` above),
its persisted logs and task data is removed automatically on saving to disk.

### Persistent Job State





## Development

### Requirements

* Go (>= 1.16)

### Running locally


```bash
go run cmd/prunner/main.go
```
> Note: for development a live reload wrapper like https://github.com/markbates/refresh is recommended.

The API should now be accessible at http://localhost:9009/. The log will contain an example JWT auth token that can be used for authentication in local development.

For interacting with the API, you need a JWT token which you can generate for developing using:

```bash
go run cmd/prunner/main.go debug
```

### Building for different operating systems.

Using the standard `GOOS` environment variable, you can build for different operating systems. This is helpful when you
want to use prunner inside a Docker container, but are developing on OSX. For this, a compile step like the following is useful:

```bash
# after building, copy the executable inside the docker container; so it can be directly used.
GOOS=linux go build cmd/prunner/main.go && docker cp main cms_neos_1:/app/main
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

An OpenAPI 2.0 spec is generated from the Go types and annoations in source code using the `go-swagger` tool (it is not
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

### UI

* Show auth errors in UI

* Idea: content ranges for polling streams


## License

GPL, because we are building on [taskctl](https://github.com/taskctl/taskctl) - see [LICENSE](LICENSE).
