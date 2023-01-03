#!/bin/bash
############################## DEV_SCRIPT_MARKER ##############################
# This script is used to document and run recurring tasks in development.     #
#                                                                             #
# You can run your tasks using the script `./dev some-task`.                  #
# You can install the Sandstorm Dev Script Runner and run your tasks from any #
# nested folder using `dev some-task`.                                        #
# https://github.com/sandstorm/Sandstorm.DevScriptRunner                      #
###############################################################################

set -e

######### TASKS #########

function build() {
  go build -o ./bin/prunner ./cmd/prunner
  _log_success "Built ./bin/prunner"
}

function memory-leak-start() {
  build


  _log_success "Starting prunner on http://127.0.0.1:9009 with profiling enabled:"
  _log_success " http://127.0.0.1:9009/debug/pprof/"
  ./bin/prunner --path test/memory_leak_debugging --verbose --enable-profiling
}

function start-pipeline {
  export PIPELINE_NAME=$1

  if [  "$PIPELINE_NAME" == "" ]; then
    _log_error "PIPELINE_NAME must be set in call to start-pipeline"
    exit 1
  fi

  _log_warning "Generating auth token"
  TOKEN=$(MINIMAL_OUTPUT=1 go run ./cmd/prunner debug)

  _log_warning "Starting pipeline $PIPELINE_NAME"

  curl -XPOST -H "Authorization: $TOKEN" -H "Content-type: application/json" -d "{
   \"pipeline\": \"$PIPELINE_NAME\"
  }" 'http://127.0.0.1:9009/pipelines/schedule'

  curl -XGET -H "Authorization: $TOKEN" -H "Content-type: application/json" \
    'http://127.0.0.1:9009/pipelines/jobs' | jq .
}

function analyze-heapdump {
  DUMPNAME=heapdump-$(date +%s)
  curl -o $DUMPNAME http://localhost:9009/debug/pprof/heap?gc=1
  #curl -o $DUMPNAME http://localhost:9009/debug/pprof/allocs
  PORT=$(jot -r 1  2000 65000)
  go tool pprof -http=:$PORT $DUMPNAME
}

####### Utilities #######

_log_success() {
  printf "\033[0;32m%s\033[0m\n" "${1}"
}
_log_warning() {
  printf "\033[1;33m%s\033[0m\n" "${1}"
}
_log_error() {
  printf "\033[0;31m%s\033[0m\n" "${1}"
}

# THIS NEEDS TO BE LAST!!!
# this will run your tasks
"$@"
