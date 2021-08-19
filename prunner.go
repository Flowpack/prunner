package prunner

import (
	"context"
	"github.com/Flowpack/prunner/store"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/apex/log"
	"github.com/friendsofgo/errors"
	"github.com/gofrs/uuid"
	"github.com/taskctl/taskctl/pkg/runner"
	"github.com/taskctl/taskctl/pkg/scheduler"
	"github.com/taskctl/taskctl/pkg/task"
	"github.com/taskctl/taskctl/pkg/variables"

	"github.com/Flowpack/prunner/definition"
	"github.com/Flowpack/prunner/helper"
	"github.com/Flowpack/prunner/taskctl"
)

// PipelineRunner is the main data structure which is basically a runtime state "singleton"
//
// All exported functions are synced with the mx mutex and are safe for concurrent use
type PipelineRunner struct {
	defs               *definition.PipelinesDef
	jobsByID           map[uuid.UUID]*PipelineJob
	jobsByPipeline     map[string][]*PipelineJob
	waitListByPipeline map[string][]*PipelineJob

	// store is the implementation for persisting data
	store store.DataStore

	// outputStore persists the log output. We need the reference here to trigger cleanup logic
	outputStore taskctl.OutputStore

	// persistRequests is for triggering saving-the-store, which is then handled asynchronously, at most every 3 seconds (see NewPipelineRunner)
	// externally, call requestPersist()
	persistRequests chan struct{}

	// Mutex for reading or writing jobs and job state
	mx               sync.RWMutex
	createTaskRunner func() taskctl.Runner
}

// NewPipelineRunner creates the central data structure which controls the full runner state; so this knows what is currently running
func NewPipelineRunner(ctx context.Context, defs *definition.PipelinesDef, createTaskRunner func() taskctl.Runner, store store.DataStore, outputStore taskctl.OutputStore) (*PipelineRunner, error) {
	pRunner := &PipelineRunner{
		defs: defs,
		// jobsByID contains ALL jobs, no matter whether they are on the waitlist or are scheduled or cancelled.
		jobsByID: make(map[uuid.UUID]*PipelineJob),
		// jobsByPipeline contains ALL jobs, no matter whether they are on the waitlist or are scheduled or cancelled.
		jobsByPipeline: make(map[string][]*PipelineJob),
		// waitListByPipeline additionally contains all the jobs currently waiting, but not yet started (because concurrency limits have been reached)
		waitListByPipeline: make(map[string][]*PipelineJob),
		store:              store,
		outputStore:        outputStore,
		// Use channel buffered with one extra slot so we can keep save requests while a save is running without blocking
		persistRequests:  make(chan struct{}, 1),
		createTaskRunner: createTaskRunner,
	}

	if store != nil {
		err := pRunner.initialLoadFromStore()
		if err != nil {
			return nil, errors.Wrap(err, "loading from store")
		}

		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-pRunner.persistRequests:
					pRunner.SaveToStore()
					// Perform save at most every 3 seconds
					time.Sleep(3 * time.Second)
				}
			}
		}()
	}

	return pRunner, nil
}

// PipelineJob is a single execution context (a single run of a single pipeline). Can be scheduled (in the waitListByPipeline of PipelineRunner),
// or currently running (jobsByID / jobsByPipeline in PipelineRunner)
type PipelineJob struct {
	ID        uuid.UUID
	Pipeline  string
	Variables map[string]interface{}

	Completed bool
	Canceled  bool
	// Created is the schedule / queue time of the job. Always non-null
	Created time.Time
	// Start is the actual start time of the job. Could be nil if not yet started.
	Start *time.Time
	// End is the actual end time of the job (can be nil if incomplete)
	End  *time.Time
	User string
	// Tasks is an in-memory representation with state of tasks, sorted by dependencies
	Tasks     jobTasks
	LastError error

	sched      *taskctl.Scheduler
	taskRunner runner.Runner
}

func (j *PipelineJob) isRunning() bool {
	return j.Start != nil && !j.Completed && !j.Canceled
}

func (r *PipelineRunner) initScheduler(j *PipelineJob) {
	// For correct cancellation of tasks a single task runner and scheduler per job is used

	taskRunner := r.createTaskRunner()

	sched := taskctl.NewScheduler(taskRunner)

	// Listen on task and stage changes for syncing the job / task state
	taskRunner.SetOnTaskChange(r.HandleTaskChange)
	sched.OnStageChange(r.HandleStageChange)

	j.taskRunner = taskRunner
	j.sched = sched
}

// deinitScheduler resets the scheduler and task runner for this job since they are no longer needed after a job is completed
func (j *PipelineJob) deinitScheduler() {
	j.sched.Finish()
	j.sched = nil
	j.taskRunner = nil
}

// jobTask is a single task invocation inside the PipelineJob
type jobTask struct {
	definition.TaskDef
	Name string

	Status   string
	Start    *time.Time
	End      *time.Time
	Skipped  bool
	ExitCode int16
	Errored  bool
	Error    error
	Canceled bool
}

type jobTasks []jobTask

type scheduleAction int

const (
	scheduleActionStart scheduleAction = iota
	scheduleActionQueue
	scheduleActionReplace
	scheduleActionNoQueue
	scheduleActionQueueFull
)

var errNoQueue = errors.New("concurrency exceeded and queueing disabled for pipeline")
var errQueueFull = errors.New("concurrency exceeded and queue limit reached for pipeline")
var ErrJobNotFound = errors.New("job not found")
var errJobAlreadyCompleted = errors.New("job is already completed")
var errJobNotStarted = errors.New("job is not started")

func (r *PipelineRunner) ScheduleAsync(pipeline string, opts ScheduleOpts) (*PipelineJob, error) {
	r.mx.Lock()
	defer r.mx.Unlock()

	pipelineDef, ok := r.defs.Pipelines[pipeline]
	if !ok {
		return nil, errors.Errorf("pipeline %q is not defined", pipeline)
	}

	action := r.resolveScheduleAction(pipeline)

	switch action {
	case scheduleActionNoQueue:
		return nil, errNoQueue
	case scheduleActionQueueFull:
		return nil, errQueueFull
	}

	id, err := uuid.NewV4()
	if err != nil {
		return nil, errors.Wrap(err, "generating job UUID")
	}

	defer r.requestPersist()

	job := &PipelineJob{
		ID:        id,
		Pipeline:  pipeline,
		Created:   time.Now(),
		Tasks:     buildJobTasks(pipelineDef.Tasks),
		Variables: opts.Variables,
		User:      opts.User,
	}

	r.jobsByID[id] = job
	r.jobsByPipeline[pipeline] = append(r.jobsByPipeline[pipeline], job)

	switch action {
	case scheduleActionQueue:
		r.waitListByPipeline[pipeline] = append(r.waitListByPipeline[pipeline], job)

		log.
			WithField("component", "runner").
			WithField("pipeline", job.Pipeline).
			WithField("jobID", job.ID).
			WithField("variables", job.Variables).
			Debugf("Queued: added job to wait list")

		return job, nil
	case scheduleActionReplace:
		waitList := r.waitListByPipeline[pipeline]
		previousJob := waitList[len(waitList)-1]
		previousJob.Canceled = true
		waitList[len(waitList)-1] = job

		log.
			WithField("component", "runner").
			WithField("pipeline", job.Pipeline).
			WithField("jobID", job.ID).
			WithField("variables", job.Variables).
			Debugf("Queued: replaced job on wait list")

		return job, nil
	}

	r.startJob(job)

	log.
		WithField("component", "runner").
		WithField("pipeline", job.Pipeline).
		WithField("jobID", job.ID).
		WithField("variables", job.Variables).
		Debugf("Started: scheduled job execution")

	return job, nil
}

func buildJobTasks(tasks map[string]definition.TaskDef) (result jobTasks) {
	result = make(jobTasks, 0, len(tasks))

	for taskName, taskDef := range tasks {
		result = append(result, jobTask{
			TaskDef: taskDef,
			Name:    taskName,
			Status:  toStatus(scheduler.StatusWaiting),
		})
	}

	result.sortTasksByDependencies()

	return result
}

func buildPipelineGraph(id uuid.UUID, tasks jobTasks, vars map[string]interface{}) (*scheduler.ExecutionGraph, error) {
	var stages []*scheduler.Stage
	for _, taskDef := range tasks {
		t := task.FromCommands(taskDef.Script...)
		t.Name = taskDef.Name
		t.AllowFailure = taskDef.AllowFailure

		taskVariables := variables.FromMap(map[string]string{
			// Inject job id for later use in the task runner (see HandleStageChange and HandleTaskChange)
			taskctl.JobIDVariableName: id.String(),
		})

		for name, value := range vars {
			if name == taskctl.JobIDVariableName {
				return nil, errors.Errorf("variable name %s is reserved for internal use", taskctl.JobIDVariableName)
			}

			taskVariables.Set(name, value)
		}

		s := &scheduler.Stage{
			Name:         taskDef.Name,
			Task:         t,
			DependsOn:    taskDef.DependsOn,
			AllowFailure: taskDef.AllowFailure,
			Variables:    taskVariables,
		}

		stages = append(stages, s)
	}

	g, err := scheduler.NewExecutionGraph(stages...)
	if err != nil {
		return nil, errors.Wrap(err, "building execution graph")
	}

	return g, nil
}

func (r *PipelineRunner) ReadJob(id uuid.UUID, process func(j *PipelineJob)) error {
	r.mx.RLock()
	defer r.mx.RUnlock()

	job, ok := r.jobsByID[id]
	if !ok {
		return ErrJobNotFound
	}

	process(job)

	return nil
}

func (r *PipelineRunner) startJob(job *PipelineJob) {
	defer r.requestPersist()

	r.initScheduler(job)

	graph, err := buildPipelineGraph(job.ID, job.Tasks, job.Variables)
	if err != nil {
		log.
			WithError(err).
			WithField("jobID", job.ID).
			WithField("pipeline", job.ID)

		job.LastError = err
		job.Canceled = true

		// A job was canceled, so there might be room for other jobs to start
		r.startJobsOnWaitList(job.Pipeline)

		return
	}

	// Actually start job
	now := time.Now()
	job.Start = &now

	// Run graph asynchronously
	go func() {
		lastErr := job.sched.Schedule(graph)
		r.JobCompleted(job.ID, lastErr)
	}()
}

// HandleTaskChange will be called when the task state changes in the task runner
func (r *PipelineRunner) HandleTaskChange(t *task.Task) {
	r.mx.Lock()
	defer r.mx.Unlock()

	jobIDString := t.Variables.Get(taskctl.JobIDVariableName).(string)
	jobID, _ := uuid.FromString(jobIDString)
	j, found := r.jobsByID[jobID]
	if !found {
		return
	}

	jt := j.Tasks.ByName(t.Name)
	if jt == nil {
		return
	}
	if !t.Start.IsZero() {
		start := t.Start
		jt.Start = &start
	}
	if !t.End.IsZero() {
		end := t.End
		jt.End = &end
	}
	jt.ExitCode = t.ExitCode
	jt.Skipped = t.Skipped

	// Set canceled flag on the job if a task was canceled through the context
	if errors.Is(t.Error, context.Canceled) {
		jt.Canceled = true
	} else {
		jt.Errored = t.Errored
		jt.Error = t.Error
	}

	// if the task has errored, and we want to fail-fast (ContinueRunningTasksAfterFailure is set to FALSE),
	// then we directly abort all other tasks of the job.
	if t.Errored {
		pipelineDef, found := r.defs.Pipelines[j.Pipeline]
		if found {
			if !pipelineDef.ContinueRunningTasksAfterFailure {
				log.
					WithField("component", "runner").
					WithField("jobID", jobIDString).
					WithField("pipeline", j.Pipeline).
					WithField("failedTaskName", t.Name).
					Debug("Task failed - cancelling all other tasks of the job")
				// we directly call cancelJobInternal; and not CancelJob, because in this method,
				// we already have the mutex (r.mx) locked - and we cannot lock it twice (otherwise
				// we'd have a deadlock)
				_ = r.cancelJobInternal(jobID)
			}
		}
	}

	r.requestPersist()
}

// HandleStageChange will be called when the stage state changes in the scheduler
func (r *PipelineRunner) HandleStageChange(stage *scheduler.Stage) {
	r.mx.Lock()
	defer r.mx.Unlock()

	jobIDString := stage.Variables.Get(taskctl.JobIDVariableName).(string)
	jobID, _ := uuid.FromString(jobIDString)
	j, ok := r.jobsByID[jobID]
	if !ok {
		return
	}

	jt := j.Tasks.ByName(stage.Name)
	if jt == nil {
		return
	}

	if jt.Canceled {
		jt.Status = "canceled"
	} else {
		jt.Status = toStatus(stage.ReadStatus())
	}

	r.requestPersist()
}

func (r *PipelineRunner) JobCompleted(id uuid.UUID, err error) {
	r.mx.Lock()
	defer r.mx.Unlock()

	job := r.jobsByID[id]
	if job == nil {
		return
	}

	job.deinitScheduler()

	job.Completed = true
	now := time.Now()
	job.End = &now
	job.LastError = err

	// Set canceled flag on the job if a task was canceled through the context
	if errors.Is(err, context.Canceled) {
		job.Canceled = true
	}

	pipeline := job.Pipeline
	log.
		WithField("component", "runner").
		WithField("jobID", id).
		WithField("pipeline", pipeline).
		Debug("Job completed")

	// A job finished, so there might be room to start other jobs on the wait list
	r.startJobsOnWaitList(pipeline)

	r.requestPersist()
}

func (r *PipelineRunner) startJobsOnWaitList(pipeline string) {
	// Check wait list if another job is queued
	waitList := r.waitListByPipeline[pipeline]

	// Schedule as many jobs as are schedulable
	for len(waitList) > 0 && r.resolveScheduleAction(pipeline) == scheduleActionStart {
		queuedJob := waitList[0]
		waitList = waitList[1:]

		r.startJob(queuedJob)

		log.
			WithField("component", "runner").
			WithField("pipeline", queuedJob.Pipeline).
			WithField("jobID", queuedJob.ID).
			Debugf("Dequeue: scheduled job execution")
	}
	r.waitListByPipeline[pipeline] = waitList
}

// IterateJobs calls process for each job in a read lock.
// It is not safe to reference the job outside of the process function.
func (r *PipelineRunner) IterateJobs(process func(j *PipelineJob)) {
	r.mx.RLock()
	defer r.mx.RUnlock()

	for _, pJob := range r.jobsByID {
		process(pJob)
	}
}

type PipelineInfo struct {
	Pipeline    string
	Schedulable bool
	Running     bool
}

// ListPipelines lists pipelines with status information about each pipeline (is it running, is it schedulable)
func (r *PipelineRunner) ListPipelines() []PipelineInfo {
	r.mx.RLock()
	defer r.mx.RUnlock()

	res := []PipelineInfo{}

	for pipeline := range r.defs.Pipelines {
		running := r.isRunning(pipeline)

		res = append(res, PipelineInfo{
			Pipeline:    pipeline,
			Schedulable: r.isSchedulable(pipeline),
			Running:     running,
		})
	}

	sort.Slice(res, func(i, j int) bool {
		return res[i].Pipeline < res[j].Pipeline
	})

	return res
}

func (r *PipelineRunner) isRunning(pipeline string) bool {
	for _, job := range r.jobsByPipeline[pipeline] {
		if job.isRunning() {
			return true
		}
	}
	return false
}

func (r *PipelineRunner) runningJobsCount(pipeline string) int {
	running := 0
	for _, job := range r.jobsByPipeline[pipeline] {
		if job.isRunning() {
			running++
		}
	}
	return running
}

func (r *PipelineRunner) resolveScheduleAction(pipeline string) scheduleAction {
	pipelineDef := r.defs.Pipelines[pipeline]

	runningJobsCount := r.runningJobsCount(pipeline)
	if runningJobsCount >= pipelineDef.Concurrency {
		// Check if jobs should be queued if concurrency factor is exceeded
		if pipelineDef.QueueLimit != nil && *pipelineDef.QueueLimit == 0 {
			return scheduleActionNoQueue
		}

		// Check if a queued job on the wait list should be replaced depending on queue strategy
		waitList := r.waitListByPipeline[pipeline]
		if pipelineDef.QueueStrategy == definition.QueueStrategyReplace && len(waitList) > 0 {
			return scheduleActionReplace
		}

		// Error if there is a queue limit and the number of queued jobs exceeds the allowed queue limit
		if pipelineDef.QueueLimit != nil && len(waitList) >= *pipelineDef.QueueLimit {
			return scheduleActionQueueFull
		}

		return scheduleActionQueue
	}

	return scheduleActionStart
}

func (r *PipelineRunner) isSchedulable(pipeline string) bool {
	action := r.resolveScheduleAction(pipeline)
	switch action {
	case scheduleActionReplace:
		fallthrough
	case scheduleActionQueue:
		fallthrough
	case scheduleActionStart:
		return true
	}
	return false
}

type ScheduleOpts struct {
	Variables map[string]interface{}
	User      string
}

func (r *PipelineRunner) initialLoadFromStore() error {
	log.
		WithField("component", "runner").
		Debug("Loading state from store")

	data, err := r.store.Load()
	if err != nil {
		return errors.Wrap(err, "loading data")
	}

	for _, pJob := range data.Jobs {
		job := buildJobFromPersistedJob(pJob)

		// Cancel job with tasks if it appears to be still running (which it cannot if we initialize from the store)
		if job.isRunning() {
			for i := range job.Tasks {
				jt := &job.Tasks[i]
				if jt.Status == "waiting" || jt.Status == "running" {
					jt.Status = "canceled"
				}
			}

			// TODO Maybe add a new "Incomplete" flag?
			job.Canceled = true

			log.
				WithField("component", "runner").
				WithField("jobID", job.ID).
				WithField("pipeline", job.Pipeline).
				Warnf("Found running job when restoring state, marked as canceled")
		}

		// Cancel jobs which have been scheduled on wait list but never been started
		if job.Start == nil {
			job.Canceled = true

			log.
				WithField("component", "runner").
				WithField("jobID", job.ID).
				WithField("pipeline", job.Pipeline).
				Warnf("Found job on wait list when restoring state, marked as canceled")
		}

		r.jobsByID[pJob.ID] = job
		r.jobsByPipeline[pJob.Pipeline] = append(r.jobsByPipeline[pJob.Pipeline], job)
	}

	return nil
}

func (r *PipelineRunner) SaveToStore() {
	log.
		WithField("component", "runner").
		Debugf("Saving job state to data store")

	r.mx.RLock()
	data := &store.PersistedData{
		Jobs: make([]store.PersistedJob, 0, len(r.jobsByID)),
	}

	// remove jobs whose retention period has expired
	for _, jobsInPipeline := range r.jobsByPipeline {
		pipelineJobBy(byCreationTimeDesc).Sort(jobsInPipeline)
		for i, job := range jobsInPipeline {
			shouldRemoveJob, removalReason := r.determineIfJobShouldBeRemoved(i, job)

			if shouldRemoveJob {
				delete(r.jobsByID, job.ID)
				r.jobsByPipeline[job.Pipeline] = removeJobFromList(r.jobsByPipeline[job.Pipeline], job)

				err := r.outputStore.Remove(job.ID.String())
				if err != nil {
					log.
						WithField("component", "runner").
						WithField("jobID", job.ID.String()).
						WithField("pipeline", job.Pipeline).
						WithField("removalReason", removalReason).
						WithError(err).
						Errorf("Removing job - Error removing Logs from Output Store for Job")
				} else {
					log.
						WithField("component", "runner").
						WithField("jobID", job.ID.String()).
						WithField("pipeline", job.Pipeline).
						WithField("removalReason", removalReason).
						Infof("Removing job")
				}
			}
		}
	}

	// convert in-memory data to the on-disk representation
	for _, job := range r.jobsByID {
		tasks := make([]store.PersistedTask, len(job.Tasks))
		for i, t := range job.Tasks {
			tasks[i] = store.PersistedTask{
				Name:         t.Name,
				Script:       t.Script,
				DependsOn:    t.DependsOn,
				AllowFailure: t.AllowFailure,
				Status:       t.Status,
				Start:        t.Start,
				End:          t.End,
				Skipped:      t.Skipped,
				ExitCode:     t.ExitCode,
				Errored:      t.Errored,
				Error:        helper.ErrToStrPtr(t.Error),
			}
		}

		data.Jobs = append(data.Jobs, store.PersistedJob{
			ID:        job.ID,
			Pipeline:  job.Pipeline,
			Completed: job.Completed,
			Canceled:  job.Canceled,
			Created:   job.Created,
			Start:     job.Start,
			End:       job.End,
			Tasks:     tasks,
			Variables: job.Variables,
			User:      job.User,
		})
	}
	r.mx.RUnlock()

	// We do not need to lock here, the single save loops guarantees non-concurrent saves

	err := r.store.Save(data)
	if err != nil {
		log.
			WithField("component", "runner").
			WithError(err).
			Errorf("Error saving job state to data store")
	}
}

// taken from https://stackoverflow.com/a/37335777
func removeJobFromList(jobs []*PipelineJob, jobToRemove *PipelineJob) []*PipelineJob {
	for index, job := range jobs {
		if job.ID == jobToRemove.ID {
			// taken from https://stackoverflow.com/a/37335777
			jobs[index] = jobs[len(jobs)-1]
			return jobs[:len(jobs)-1]
		}
	}

	// not found, we return the full list
	return jobs
}

// determineIfJobShouldBeRemoved implements the retention period handling.
func (r *PipelineRunner) determineIfJobShouldBeRemoved(index int, job *PipelineJob) (bool, string) {
	pipelineDef, pipelineDefExists := r.defs.Pipelines[job.Pipeline]
	if !pipelineDefExists {
		return true, "Pipeline Definition not found"
	}

	if job.Start == nil && !job.Canceled {
		// always keep jobs of waitlist.
		return false, "keeping job of waitlist"
	}

	if !job.Completed && !job.Canceled {
		// always keep jobs which are not yet in some "finished" state.
		return false, "keeping non-finished jobs"
	}

	if pipelineDef.RetentionPeriodHours > 0 && -time.Until(job.Created) > time.Duration(pipelineDef.RetentionPeriodHours)*time.Hour {
		return true, "Retention Period of " + strconv.Itoa(pipelineDef.RetentionPeriodHours) + " hours reached"
	}

	if pipelineDef.RetentionCount > 0 && index >= pipelineDef.RetentionCount {
		return true, "Retention Count of " + strconv.Itoa(pipelineDef.RetentionCount) + " reached"
	}

	return false, ""
}

func (r *PipelineRunner) requestPersist() {
	// Debounce persist requests by not sending if the persist channel is already full (buffered with length 1)
	select {
	case r.persistRequests <- struct{}{}:
		// The default case prevents blocking when sending to a full channel
	default:
	}
}

func (r *PipelineRunner) CancelJob(id uuid.UUID) error {
	r.mx.Lock()
	defer r.mx.Unlock()

	return r.cancelJobInternal(id)
}

func (r *PipelineRunner) cancelJobInternal(id uuid.UUID) error {
	job, ok := r.jobsByID[id]
	if !ok {
		return ErrJobNotFound
	}

	if job.Completed {
		return errJobAlreadyCompleted
	}

	if job.Start == nil {
		return errJobNotStarted
	}
	log.
		WithField("component", "runner").
		WithField("pipeline", job.Pipeline).
		WithField("jobID", job.ID).
		Debugf("Canceling job")

	go (func() {
		if job.sched != nil {
			job.sched.Cancel()
		}
	})()

	return nil
}

func buildJobFromPersistedJob(pJob store.PersistedJob) *PipelineJob {
	job := &PipelineJob{
		ID:        pJob.ID,
		Pipeline:  pJob.Pipeline,
		Completed: pJob.Completed,
		Canceled:  pJob.Canceled,
		Created:   pJob.Created,
		Start:     pJob.Start,
		End:       pJob.End,
		Variables: pJob.Variables,
		User:      pJob.User,
	}

	tasks := make(jobTasks, len(pJob.Tasks))
	for i, pJobTask := range pJob.Tasks {
		tasks[i] = jobTask{
			Name: pJobTask.Name,
			TaskDef: definition.TaskDef{
				Script:       pJobTask.Script,
				DependsOn:    pJobTask.DependsOn,
				AllowFailure: pJobTask.AllowFailure,
			},
			Status:   pJobTask.Status,
			Start:    pJobTask.Start,
			End:      pJobTask.End,
			Skipped:  pJobTask.Skipped,
			ExitCode: pJobTask.ExitCode,
			Errored:  pJobTask.Errored,
			Error:    helper.StrPtrToErr(pJobTask.Error),
		}
	}
	job.Tasks = tasks

	return job
}

// sortTasksByDependencies is used only for the UI, to have a stable sorting
func (jt jobTasks) sortTasksByDependencies() {
	// Apply topological sorting (see https://en.wikipedia.org/wiki/Topological_sorting#Kahn's_algorithm)

	var queue []string

	type tmpNode struct {
		name     string
		incoming map[string]struct{}
		order    int
	}

	tmpNodes := make(map[string]*tmpNode)

	// Build temporary graph
	for _, n := range jt {
		inc := make(map[string]struct{})
		for _, from := range n.DependsOn {
			inc[from] = struct{}{}
		}
		tmpNodes[n.Name] = &tmpNode{
			name:     n.Name,
			incoming: inc,
		}
		if len(inc) == 0 {
			queue = append(queue, n.Name)
		}
	}
	// Make sure a stable sorting is used for the traversal of nodes (map has no defined order)
	sort.Strings(queue)

	i := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]

		tmpNodes[n].order = i
		i++

		for _, m := range tmpNodes {
			if _, exist := m.incoming[n]; exist {
				delete(m.incoming, n)

				if len(m.incoming) == 0 {
					queue = append(queue, m.name)
				}
			}
		}
		sort.Strings(queue)
	}

	sort.Slice(jt, func(i, j int) bool {
		ri := tmpNodes[jt[i].Name].order
		rj := tmpNodes[jt[j].Name].order
		// For same rank order by name
		if ri == rj {
			return jt[i].Name < jt[j].Name
		}
		// Otherwise order by rank
		return ri < rj
	})
}

func (jt jobTasks) ByName(name string) *jobTask {
	for i := range jt {
		if jt[i].Name == name {
			return &jt[i]
		}
	}
	return nil
}

func toStatus(status int32) string {
	switch status {
	case scheduler.StatusWaiting:
		return "waiting"
	case scheduler.StatusRunning:
		return "running"
	case scheduler.StatusSkipped:
		return "skipped"
	case scheduler.StatusDone:
		return "done"
	case scheduler.StatusError:
		return "error"
	case scheduler.StatusCanceled:
		return "canceled"
	}
	return ""
}
