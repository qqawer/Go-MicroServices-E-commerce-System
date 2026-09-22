#!/usr/bin/env sh
set -eu
# mqadmin can exit 0 after a Java exception: verify its output and the route.
for topic in order-reback order_timeout; do
  ready=false
  for attempt in 1 2 3 4 5 6 7 8 9 10; do
    output=$(sh mqadmin updateTopic -n mqnamesrv:9876 -b mqbroker:10911 -t "$topic" -r 4 -w 4 2>&1)
    if echo "$output" | grep -q 'create topic to .* success'; then ready=true; break; fi
    sleep 2
  done
  if [ "$ready" != true ]; then echo "Cannot create topic: $topic" >&2; exit 1; fi
  ready=false
  for attempt in 1 2 3 4 5 6 7 8 9 10; do
    output=$(sh mqadmin topicRoute -n mqnamesrv:9876 -t "$topic" 2>&1)
    if echo "$output" | grep -q 'brokerAddrs'; then ready=true; break; fi
    sleep 2
  done
  if [ "$ready" != true ]; then echo "Cannot resolve topic: $topic" >&2; exit 1; fi
  echo "Verified topic: $topic"
done
