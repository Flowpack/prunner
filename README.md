# Pipeline Runner

**An embeddable task / pipeline runner with a HTTP API.**

- Good for orchestrating long-running jobs with multiple steps; so if you think "I need a CI pipeline" but within your project, this is for you.
- The pipeline definition is done in a `pipelines.yml` file and is static.
- To start a new job, you use an authenticated HTTP API.
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


## Development

### Requirements

* Go (>= 1.16)
* Node.js (>= 12)
* Yarn

### Running locally

#### prunner

```bash
cd prunner
go run .
```
> Note: for development a live reload wrapper like https://github.com/markbates/refresh is recommended.

The API should now be accessible at http://localhost:9009/. The log will contain an example JWT auth token that can be used for authentication in local development.

#### prunner-ui

```bash
cd prunner-ui
yarn install
SNOWPACK_PUBLIC_API_AUTH_TOKEN=[Example JWT token] yarn start
```

The UI should now be accessible at http://localhost:8080/.

## Security concept

* The HTTP server only listens on localhost by default
* prunner always enables authentication via JWT
* An application that wants to embed prunner should read the shared secret and generate a JWT auth token for accessing the API by
  doing internal HTTP requests. This way custom policies can be implemented for ensuring access to prunner.

## TODO

* Lockfile to prevent concurrent run of Prunner?
* (1) Concurrency strategy for pipelines:
  * concurrency factor + wait list size (0, bounded, unbounded) ✅
  * queue (with limit) ✅
  * replace queued jobs ✅
    * optional: interrupt running
  * append / replace strategy for queueing ✅
  * variable append / replace strategy (i.e. todo list)
    * to be defined: is this about variables being replaced/appended in replaced jobs?
  * debounced queue -> Add delay before job can start
* Interruptible tasks?
* Graceful shutdown
  * Pipeline wait time
* (3) on_error
* Cancellation of running pipelines / tasks
  * `context.Context` should be supplied when scheduling pipelines
* (2) Persist pipeline runs and results
  * Store as JSON and save asynchronously ✅
  * Store output in files separate from metadata ✅
  * Cleanup of output after X days / runs
* Recover state of broken pipelines (after prunner crash) ✅
* Correct handling of stdout/stderr
  * Store task output in files ✅
  * Provide concurrent safe in-memory output store for running tasks ✅
  * Streaming of task stdout/stderr to API
* (4) Pass variables to pipelines on schedule
* Timeout per task
* Cron jobs
  * static from YAML
  * dynamic via API
* Generate OpenAPI spec for prunner API from Go code
* remove finished jobs after X days (or keep X per pipeline)

### UI

* Show auth errors in UI

* Idea: content ranges for polling streams


## License

GPL, because we are building on [taskctl](https://github.com/taskctl/taskctl) - see [LICENSE](LICENSE).