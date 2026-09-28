"""Capacity gates for the Docker daemon host, including Jenkins-in-container runs."""
import re

BROKER = 'grpctest-infra-mqbroker-1'
HEALTH_TOPIC = 'grpctest-healthcheck'


def check_disk(run, minimum_free_gib=3):
    # Inspect the actual MQ filesystem, not the Jenkins container writable layer.
    output = run(['docker', 'exec', BROKER, 'df', '-Pk', '/home/rocketmq/store'], capture=True).stdout
    rows = output.strip().splitlines()
    fields = rows[-1].split() if len(rows) >= 2 else []
    if len(fields) != 6 or not re.fullmatch(r'\d+%', fields[4]):
        raise ValueError('Cannot determine broker disk capacity; deployment stopped')
    free = int(fields[3]) * 1024
    used = int(fields[4][:-1])
    if used >= 85 or free < minimum_free_gib * 1024**3:
        raise ValueError(f'Broker disk pressure: {used}% used, {free / 1024**3:.2f} GiB free; '
                         f'require <85% and >= {minimum_free_gib} GiB before rollout')
    print(f'Broker disk: {used}% used, {free / 1024**3:.2f} GiB free')


def check_mq_write(run):
    # Only this dedicated topic receives probes; no order or inventory messages.
    command = ['docker', 'exec', BROKER, 'sh', '-c',
               'sh "$ROCKETMQ_HOME/bin/mqadmin" "$@"', 'sh',
               'sendMessage', '-n', 'mqnamesrv:9876', '-t', HEALTH_TOPIC,
               '-b', 'grpctest-broker', '-i', '0', '-p', 'grpctest-deployment-check']
    output = run(command, capture=True).stdout
    # mqadmin can return exit 0 on Java failure. Also reject flush timeouts.
    if not re.search(r'^grpctest-broker\s+0\s+SEND_OK\s+\S+\s*$', output, re.MULTILINE):
        raise ValueError('RocketMQ did not confirm SEND_OK on grpctest-healthcheck; deployment stopped')
    print('RocketMQ dedicated-topic write: SEND_OK')
