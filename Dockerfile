# syntax=docker/dockerfile:1

# The Go build runs natively and cross-compiles; only the runtime stage runs
# under emulation for the other architecture.
FROM --platform=$BUILDPLATFORM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/calibruh ./cmd/calibruh

FROM debian:bookworm-slim

# calibredb only. Keep this in step with the calibre that edits the
# libraries elsewhere, so it never meets a newer schema than it knows.
ARG CALIBRE_VERSION=9.2.1
ARG TARGETARCH
# The calibre binaries link against Qt and its X/GL dependencies even
# when nothing is displayed.
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl xz-utils \
      libegl1 libopengl0 libgl1 libglx0 libxcb-cursor0 libxkbcommon0 libxkbcommon-x11-0 \
      libfontconfig1 libfreetype6 libnss3 libxcomposite1 libxdamage1 libxrandr2 libxtst6 libxi6 libasound2 \
 && case "$TARGETARCH" in amd64) arch=x86_64 ;; arm64) arch=arm64 ;; *) echo "unsupported arch $TARGETARCH" >&2; exit 1 ;; esac \
 && mkdir -p /opt/calibre \
 && curl -fsSL "https://download.calibre-ebook.com/${CALIBRE_VERSION}/calibre-${CALIBRE_VERSION}-${arch}.txz" | tar -xJ -C /opt/calibre \
 && apt-get purge -y curl xz-utils && apt-get autoremove -y \
 && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/calibruh /usr/local/bin/calibruh
COPY <<'EOF' /etc/calibruh.json
{
  "addr": ":8080",
  "calibredb": "/opt/calibre/calibredb",
  "libraries_dir": "/data"
}
EOF

# calibre writes its config under HOME; /tmp works for any --user.
ENV CALIBRUH_CONFIG=/etc/calibruh.json \
    HOME=/tmp \
    QT_QPA_PLATFORM=offscreen

USER 1000:1000
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/calibruh"]
