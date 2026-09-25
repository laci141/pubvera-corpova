# syntax=docker/dockerfile:1
#
# Multi-stage build for scientific-consensus-web.
#
# Stage 1 builds the scientific-consensus CLI with go install from one pinned
# upstream printing-press-library commit. It used to be a PRE-BUILT
# linux/amd64 binary (bin/scientific-consensus-pp-cli-linux) made by
# vendor-cli.sh, and nothing in the image said which upstream source it came
# from. The commit is now stamped on the image as org.pubvera.cli.commit.
#
# PP_LIBRARY_COMMIT is declared before the first FROM so it is global. An ARG
# declared after a FROM exists only in that stage; each stage that needs the
# value re-declares it with a bare ARG and inherits this default. Declaring
# the default inside the builder stage only left the label empty on
# pubvera-recallis (measured 2026-09-24), and CI now fails on that.
#
# Stage 2 builds the web server for linux/amd64 from all root *.go files
# (main.go + providers.go).
ARG PP_LIBRARY_COMMIT=58edea349ce3df8a301d4d8950119487c32604b8

# ---- Stage 1: build the CLI from upstream source -----------------------------
FROM golang:1.26-alpine AS cli-builder
ARG PP_LIBRARY_COMMIT
RUN CGO_ENABLED=0 go install -trimpath \
    github.com/mvanhorn/printing-press-library/library/other/scientific-consensus/cmd/scientific-consensus-pp-cli@${PP_LIBRARY_COMMIT}

# ---- Stage 2: build the web server ------------------------------------------
# The web module is stdlib-only, so it has no go.sum and `go mod download` is a
# no-op — copy just go.mod.
FROM golang:1.26-alpine AS web-builder
WORKDIR /build
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/server .

# ---- Stage 3: minimal runtime ------------------------------------------------
FROM alpine:latest
# ca-certificates: the CLI makes HTTPS calls to PubMed/OpenAlex/Crossref and,
# when a BYOK key is supplied, to the LLM providers.
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
WORKDIR /app
COPY --from=web-builder /out/server ./server
COPY --from=cli-builder /go/bin/scientific-consensus-pp-cli ./bin/scientific-consensus-pp-cli
COPY bin/scientific-consensus-pp-cli-linux ./mutation-check
COPY index.html ./index.html
RUN chmod +x ./bin/scientific-consensus-pp-cli

# The upstream commit the CLI was built from, readable with docker inspect.
ARG PP_LIBRARY_COMMIT
LABEL org.pubvera.cli.commit=${PP_LIBRARY_COMMIT}

ENV CLI_BIN=/app/bin/scientific-consensus-pp-cli
# The server binds 0.0.0.0:$PORT when Render sets $PORT; locally it defaults to
# 127.0.0.1:8090.
EXPOSE 8090
USER app
CMD ["./server"]