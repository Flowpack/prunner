# FEATURE: Partitioned Waitlists


# Problem description

## Current state

> **FROM README**
>
> By default, if you limit concurrency, and the limit is exceeded, further jobs are added to the
> waitlist of the pipeline.
> 
> However, you have some options to configure this as well:
> 
> The waitlist can have a maximum size, denoted by `queue_limit`:
> 
> ```yaml
> pipelines:
>   do_something:
>     queue_limit: 1
>     concurrency: 1
>     tasks: # as usual
> ```
> 
> To deactivate the queuing altogether, set `queue_limit: 0`.
> 
> Now, if the queue is limited, an error occurs when it is full and you try to add a new job.
> 
> Alternatively, you can also set `queue_strategy: replace` to replace the last job in the
> queue by the newly added one:

## Current state -> Problem "Starvation" with "Debounce" ??

> Queues a run of the job and only starts it after 10 seconds have passed (if no other run was triggered which replaced the queued job)

```
--> time (1 char = 1 s)

# 1 waitlist slot, delay 10 s

a____ _____ <-- here the job A is queued
          A <-- here job A starts


# 1 waitlist slot, delay 10 s
# STARVATION CAN HAPPEN
a____ b____ c_____ _____
                       C
                       

# 2 waitlist slots, delay 10 s
# !!! PREVENTS STARVATION !!!
a____ b__c_ ______ _____
[a_]  [ab]  A              <-- here, a starts. NO STARVATION 
         [ac]              
            [c_]
```

SUMMARY: 
- Starvation can only happen if waitlist size=1; if waitlist size=2 (or bigger) cannot happen because always the LAST job gets replaced.
- We cannot debounce immediately; so we ALWAYS wait at least for start_delay. (not a problem for us right now).
 
## problem description

In a project, we have 50 Neos instances, which use prunner for background tasks (some are short, some are long).

Currently, we have 4 pipelines globally
- concurrency 1
- concurrency 8
- concurrency 4
- concurrency 4 (import job)
  - -> needs the global concurrency to limit the needed server resources

Now, a new pipeline should be added for "irregular import jobs" triggered by webhooks.
- can happen very quickly after each other
- -> Pipeline should start after a certain amount of time (newer should override older pipelines)
- StartDelay combined with QueueStrategy "Replace"
  - `waitList[len(waitList)-1] = job` -> *LAST* Element is replaced of wait list.
- -> GLOBAL replacement does not work, because each job has arguments (which are relevant, i.e. tenant ID).

We still want to have a GLOBAL CONCURRENCY LIMIT (per pipeline), but a WAITLIST per instance.


## Solution Idea:

we want to be able to SEGMENT the waitlist into different partitions. The `queue_strategy` and `queue_limit` should be per partition.
`concurrency` stays per pipeline (as before)

```
**LOGICAL VIEW** (Idea)
┌──────────────────────┐      ┌──────────────────────────────────────────────────┐
│ Waitlist Instance 1  │      │          Pipeline (with concurrency 2)           │
├──────────────────────┤      │                                                  │
│ Waitlist Instance 2  │  ->  ├──────────────────────────────────────────────────┤
├──────────────────────┤      │                                                  │
│ Waitlist Instance 3  │      │                                                  │
└──────────────────────┘      └──────────────────────────────────────────────────┘
                                                                                  
          if job is *delayed*,                                                    
           stays in wait list                                                     
            for this duration                                                     
                                                                                  
```

Technically, we still have a SINGLE Wait List per pipeline, but the jobs can be partitioned by `waitlist_partition_id`.

-> In this case, the `replace` strategy will replace the last element of the given partition.

-> we create a new queueStrategy for the partitioned waitlist: `partitioned_replace`

If we partition the waitlist, the waitlist can grow up to queue_limit * number of partitions.