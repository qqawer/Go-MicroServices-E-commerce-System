# Fixed Apache RocketMQ Go SDK snapshot

Origin: https://github.com/apache/rocketmq-client-go/tree/99c433634e09f72fa2778ca04411de29d4fc9cff

The application replaces the pinned Go module with this runtime-source snapshot. LICENSE and NOTICE are preserved. Examples, benchmarks, documentation and upstream tests are omitted. UPSTREAM.json records every copied file's original SHA-256. Apart from `consumer/process_queue.go`, upstream runtime source is unchanged.

The real-broker `-race` suite reproduced an unlocked cache-size read in `cleanExpiredMsg` and an unlocked cache-empty read in `takeMessages`. Both race with `putMessage`/`removeMessage`. This snapshot locks those two reads using the existing processQueue mutex. The upstream subscription lock fix is already part of the pinned commit.

`consumer/local_cache_race_test.go` reproduces the cache race without a broker (failed before the correction, passes after). Run from the application root:

```
GOWORK=off go test -race -mod=readonly github.com/apache/rocketmq-client-go/v2/consumer -run '^TestLocal'
```

The application uses normal concurrent consumption and explicit callback draining during shutdown. Do not disable race detection, extend cleanup timers to hide the race, or silently update this snapshot. On a future SDK upgrade, compare the lock fixes and rerun the real-broker outage/restart suite before removing this replacement.
