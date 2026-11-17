# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/recallroute ./cmd/recallroute

# Static binary on a distroless base: no shell, no package manager,
# runs as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/recallroute /recallroute
ENV RECALLROUTE_ADDR=:8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/recallroute"]
