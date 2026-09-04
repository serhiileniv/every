# The systemd toolchain on a pinned base, so `systemd-analyze calendar` can
# validate the OnCalendar expressions every generates against a known version
# rather than whatever the CI runner happens to ship.
#
# systemd-analyze is a pure validator here -- it needs the binary, not PID 1 --
# so a plain image (no init) is enough. The repo is mounted at run time;
# nothing is COPYed in, which keeps this image code-agnostic and reusable.
FROM golang:1.25-bookworm

RUN apt-get update \
 && apt-get install -y --no-install-recommends systemd \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /app
