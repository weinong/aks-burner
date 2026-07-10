FROM ubuntu:24.04

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
       bash \
       ca-certificates \
       coreutils \
       curl \
       fio \
       git \
       jq \
       time \
    && rm -rf /var/lib/apt/lists/*

COPY scripts/run-fio.sh /usr/local/bin/run-fio.sh
COPY scripts/run-git-clone.sh /usr/local/bin/run-git-clone.sh
COPY fio-profiles /profiles

RUN chmod +x /usr/local/bin/run-fio.sh /usr/local/bin/run-git-clone.sh
