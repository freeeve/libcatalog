# lcatd container image -- the self-host / Cloud Run / k8s deployment shape.
# Build from the repo root:
#   docker build -f backend/deploy/docker/Dockerfile -t lcatd .
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY backend/go.mod backend/go.sum ./backend/
COPY . .
RUN cd backend && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lcatd ./cmd/lcatd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/lcatd /lcatd
EXPOSE 8080
ENTRYPOINT ["/lcatd"]
