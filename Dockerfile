FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /agentops ./cmd/agentops

FROM scratch
COPY --from=build /agentops /agentops
COPY runs /runs
ENV AGENTOPS_RUNS=/runs TERM=xterm-256color
ENTRYPOINT ["/agentops"]
