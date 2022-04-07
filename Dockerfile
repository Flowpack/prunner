FROM alpine:3.15
ENTRYPOINT ["/prunner"]
STOPSIGNAL SIGINT
COPY prunner /prunner
