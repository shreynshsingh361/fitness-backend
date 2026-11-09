# Stage 1: compile the Go program.
FROM golang:1.27-alpine AS build
WORKDIR /src
# Copy go.mod/go.sum first so Docker can cache the dependency download
# until they change.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/api

# Stage 2: a tiny image that contains only the compiled program.
FROM alpine:3
RUN adduser -D -u 10001 app
USER app
COPY --from=build /out/server /server
EXPOSE 8081
ENTRYPOINT ["/server"]
