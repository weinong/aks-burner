FROM ubuntu:24.04@sha256:4fbb8e6a8395de5a7550b33509421a2bafbc0aab6c06ba2cef9ebffbc7092d90

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
       bash \
       ca-certificates \
       coreutils \
       curl \
       e2fsprogs \
       fio \
       git \
       jq \
       mount \
       time \
       util-linux \
    && rm -rf /var/lib/apt/lists/*

COPY scripts/override /usr/local/bin/override
COPY scripts/run-block-benchmark.sh /usr/local/bin/run-block-benchmark.sh
COPY scripts/run-fio.sh /usr/local/bin/run-fio.sh
COPY scripts/run-git-clone.sh /usr/local/bin/run-git-clone.sh
COPY fio-profiles /profiles

RUN chmod +x /usr/local/bin/override /usr/local/bin/run-fio.sh /usr/local/bin/run-git-clone.sh /usr/local/bin/run-block-benchmark.sh
