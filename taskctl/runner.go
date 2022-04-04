package taskctl

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/apex/log"
	"github.com/taskctl/taskctl/pkg/executor"
	"github.com/taskctl/taskctl/pkg/output"
	"github.com/taskctl/taskctl/pkg/runner"
	"github.com/taskctl/taskctl/pkg/task"
	"github.com/taskctl/taskctl/pkg/variables"
)

const JobIDVariableName = "__jobID"

// Runner extends runner.Runner (from taskctl) to implement additional features:
// - storage of outputs
// - callback on finished task (so we can f.e. schedule the next task when one is on the wait-queue)
// The file https://github.com/taskctl/taskctl/blob/master/pkg/runner/runner.go is the original basis of this file.
type Runner interface {
	runner.Runner
	SetOnTaskChange(func(t *task.Task))
}

// TaskRunner run tasks
type TaskRunner struct {
	contexts  map[string]*runner.ExecutionContext
	variables variables.Container
	env       variables.Container

	ctx         context.Context
	cancelFunc  context.CancelFunc
	cancelMutex sync.RWMutex
	canceling   bool
	wg          sync.WaitGroup

	compiler *runner.TaskCompiler

	Stdin          io.Reader
	Stdout, Stderr io.Writer
	OutputFormat   string

	cleanupList sync.Map

	// Additional output store to store task outputs
	outputStore OutputStore

	onTaskChange func(t *task.Task)

	killTimeout time.Duration
}

var _ Runner = &TaskRunner{}

// NewTaskRunner creates new TaskRunner instance
func NewTaskRunner(outputStore OutputStore, opts ...Opts) (*TaskRunner, error) {
	r := &TaskRunner{
		compiler:     runner.NewTaskCompiler(),
		OutputFormat: output.FormatRaw,
		Stdin:        os.Stdin,
		Stdout:       os.Stdout,
		Stderr:       os.Stderr,
		variables:    variables.NewVariables(),
		env:          variables.NewVariables(),

		outputStore: outputStore,

		killTimeout: 2 * time.Second,
	}

	r.ctx, r.cancelFunc = context.WithCancel(context.Background())

	for _, o := range opts {
		o(r)
	}

	r.env.Merge(variables.FromMap(map[string]string{"ARGS": r.variables.Get("Args").(string)}))

	return r, nil
}

// SetOnTaskChange is synchronous; so you can read the task in any way inside the callback because there is no concurrent access
// of the task
func (r *TaskRunner) SetOnTaskChange(f func(t *task.Task)) {
	r.onTaskChange = f
}

// SetContexts sets task runner's contexts
func (r *TaskRunner) SetContexts(contexts map[string]*runner.ExecutionContext) *TaskRunner {
	r.contexts = contexts

	return r
}

// SetVariables sets task runner's variables
func (r *TaskRunner) SetVariables(vars variables.Container) *TaskRunner {
	r.variables = vars

	return r
}

// Run run provided task.
// TaskRunner first compiles task into linked list of Jobs, then passes those jobs to Executor
func (r *TaskRunner) Run(t *task.Task) error {
	// Keep track of running tasks for graceful shutdown and waiting until all tasks are canceled
	r.wg.Add(1)
	defer r.wg.Done()

	if err := r.ctx.Err(); err != nil {
		return err
	}

	execContext, err := r.contextForTask(t)
	if err != nil {
		return err
	}

	var stdin io.Reader
	if t.Interactive {
		stdin = r.Stdin
	}

	defer func() {
		err = execContext.After()
		if err != nil {
			log.Errorf("Error executing after tasks: %v", err)
		}

		if !t.Errored && !t.Skipped {
			t.ExitCode = 0
		}
	}()

	vars := r.variables.Merge(t.Variables)

	env := r.env.Merge(execContext.Env)
	env = env.With("TASK_NAME", t.Name)
	env = env.Merge(t.Env)

	jobID := t.Variables.Get(JobIDVariableName).(string)

	meets, err := r.checkTaskCondition(t)
	if err != nil {
		return err
	}

	if !meets {
		log.
			WithField("component", "runner").
			WithField("jobID", jobID).
			Infof("Task %s was skipped", t.Name)
		t.Skipped = true
		return nil
	}

	err = r.before(r.ctx, t, env, vars)
	if err != nil {
		return err
	}

	var (
		stdoutWriter = []io.Writer{&t.Log.Stdout}
		stderrWriter = []io.Writer{&t.Log.Stderr}
	)
	if r.outputStore != nil {
		{
			stdoutStorer, err := r.outputStore.Writer(jobID, t.Name, "stdout")
			if err != nil {
				return err
			}
			defer func() {
				stdoutStorer.Close()
			}()
			stdoutWriter = append(stdoutWriter, stdoutStorer)
		}

		{
			stderrStorer, err := r.outputStore.Writer(jobID, t.Name, "stderr")
			if err != nil {
				return err
			}
			defer func() {
				stderrStorer.Close()
			}()
			stderrWriter = append(stderrWriter, stderrStorer)
		}
	}

	job, err := r.compiler.CompileTask(
		t,
		execContext,
		stdin,
		io.MultiWriter(stdoutWriter...),
		io.MultiWriter(stderrWriter...),
		env,
		vars,
	)
	if err != nil {
		return err
	}

	if job != nil {
		err = r.execute(r.ctx, t, job)
		if err != nil {
			return err
		}
		r.storeTaskOutput(t)
	}

	return r.after(r.ctx, t, env, vars)
}

// Cancel cancels execution
func (r *TaskRunner) Cancel() {
	r.cancelMutex.Lock()
	if !r.canceling {
		r.canceling = true
		defer log.
			WithField("component", "runner").
			Debug("Task runner has been cancelled")
		r.cancelFunc()
	}
	r.cancelMutex.Unlock()
	// Wait until all running tasks are canceled and return
	r.wg.Wait()
}

// Finish makes cleanup tasks over contexts
func (r *TaskRunner) Finish() {
	r.cleanupList.Range(func(key, value interface{}) bool {
		value.(*runner.ExecutionContext).Down()
		return true
	})
	output.Close()
}

// WithVariable adds variable to task runner's variables list.
// It creates new instance of variables container.
func (r *TaskRunner) WithVariable(key, value string) *TaskRunner {
	r.variables = r.variables.With(key, value)

	return r
}

func (r *TaskRunner) before(ctx context.Context, t *task.Task, env, vars variables.Container) error {
	if len(t.Before) == 0 {
		return nil
	}

	execContext, err := r.contextForTask(t)
	if err != nil {
		return err
	}

	for _, command := range t.Before {
		job, err := r.compiler.CompileCommand(command, execContext, t.Dir, t.Timeout, nil, r.Stdout, r.Stderr, env, vars)
		if err != nil {
			return fmt.Errorf("\"before\" command compilation failed: %w", err)
		}

		exec, err := NewPgidExecutor(job.Stdin, job.Stdout, job.Stderr, r.killTimeout)
		if err != nil {
			return err
		}

		_, err = exec.Execute(ctx, job)
		if err != nil {
			return err
		}
	}

	return nil
}

func (r *TaskRunner) after(ctx context.Context, t *task.Task, env, vars variables.Container) error {
	if len(t.After) == 0 {
		return nil
	}

	execContext, err := r.contextForTask(t)
	if err != nil {
		return err
	}

	for _, command := range t.After {
		job, err := r.compiler.CompileCommand(command, execContext, t.Dir, t.Timeout, nil, r.Stdout, r.Stderr, env, vars)
		if err != nil {
			return fmt.Errorf("\"after\" command compilation failed: %w", err)
		}

		exec, err := NewPgidExecutor(job.Stdin, job.Stdout, job.Stderr, r.killTimeout)
		if err != nil {
			return err
		}

		_, err = exec.Execute(ctx, job)
		if err != nil {
			log.
				WithField("component", "runner").
				WithField("command", command).
				Warnf("After command failed: %v", err)
		}
	}

	return nil
}

func (r *TaskRunner) contextForTask(t *task.Task) (c *runner.ExecutionContext, err error) {
	if t.Context == "" {
		c = runner.DefaultContext()
	} else {
		var ok bool
		c, ok = r.contexts[t.Context]
		if !ok {
			return nil, fmt.Errorf("no such context %s", t.Context)
		}

		r.cleanupList.Store(t.Context, c)
	}

	err = c.Up()
	if err != nil {
		return nil, err
	}

	err = c.Before()
	if err != nil {
		return nil, err
	}

	return c, nil
}

func (r *TaskRunner) checkTaskCondition(t *task.Task) (bool, error) {
	if t.Condition == "" {
		return true, nil
	}

	executionContext, err := r.contextForTask(t)
	if err != nil {
		return false, err
	}

	job, err := r.compiler.CompileCommand(t.Condition, executionContext, t.Dir, t.Timeout, nil, r.Stdout, r.Stderr, r.env, r.variables)
	if err != nil {
		return false, err
	}

	exec, err := NewPgidExecutor(job.Stdin, job.Stdout, job.Stderr, r.killTimeout)
	if err != nil {
		return false, err
	}

	_, err = exec.Execute(context.Background(), job)
	if err != nil {
		if _, ok := executor.IsExitStatus(err); ok {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

func (r *TaskRunner) storeTaskOutput(t *task.Task) {
	varName := fmt.Sprintf("Tasks.%s.Output", strings.Title(t.Name))

	// we need to disable storing the task output as environment variable; otherwise we have
	// quite serious limits how much we can execute.
	// This seems to be the problem https://stackoverflow.com/a/1078125
	// "The total size of all the environment variables put together is limited at execve() time.
	// See "Limits on size of arguments and environment" at https://man7.org/linux/man-pages/man2/execve.2.html for more information.
	// On some systems here, this is around 2 MB (not enough!)
	r.variables.Set(varName, t.Log.Stdout.String())
}

func (r *TaskRunner) execute(ctx context.Context, t *task.Task, job *executor.Job) error {
	exec, err := NewPgidExecutor(job.Stdin, job.Stdout, job.Stderr, r.killTimeout)
	if err != nil {
		return err
	}

	t.Start = time.Now()
	r.notifyTaskChange(t)

	var prevOutput []byte
	for nextJob := job; nextJob != nil; nextJob = nextJob.Next {
		var err error
		nextJob.Vars.Set("Output", string(prevOutput))

		prevOutput, err = exec.Execute(ctx, nextJob)
		if err != nil {
			if status, ok := executor.IsExitStatus(err); ok {
				t.ExitCode = int16(status)
				if t.AllowFailure {
					r.notifyTaskChange(t)
					continue
				}
			}
			t.Errored = true
			t.Error = err
			r.notifyTaskChange(t)
			return t.Error
		}
	}
	t.End = time.Now()
	r.notifyTaskChange(t)

	return nil
}

func (r *TaskRunner) notifyTaskChange(t *task.Task) {
	if r.onTaskChange != nil {
		r.onTaskChange(t)
	}
}

// Opts is a task runner configuration function.
type Opts func(*TaskRunner)

// WithContexts adds provided contexts to task runner
func WithContexts(contexts map[string]*runner.ExecutionContext) Opts {
	return func(runner *TaskRunner) {
		runner.contexts = contexts
	}
}

// WithEnv adds provided environment variables to task runner
func WithEnv(env variables.Container) Opts {
	return func(runner *TaskRunner) {
		runner.env = env
	}
}

// WithVariables adds provided variables to task runner
func WithVariables(variables variables.Container) Opts {
	return func(runner *TaskRunner) {
		runner.variables = variables
		// TODO The variables field is not exported
		// runner.compiler.variables = variables
	}
}

// WithKillTimeout sets the kill timeout for execution of commands
func WithKillTimeout(killTimeout time.Duration) Opts {
	return func(runner *TaskRunner) {
		runner.killTimeout = killTimeout
	}
}
