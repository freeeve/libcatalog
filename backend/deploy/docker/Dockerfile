# lcatd container image -- the self-host / Cloud Run / k8s deployment shape.
# Build from the repo root:
#   docker build -f backend/deploy/docker/Dockerfile -t lcatd .

# SPA build stage: produce the real dist/ that go:embed compiles into lcatd.
# Without this the Go build would embed the committed placeholder index.html and
# the browser UI would not boot (tasks/098).
FROM node:22 AS ui
WORKDIR /ui
COPY backend/ui/package.json backend/ui/package-lock.json ./
RUN npm ci
COPY backend/ui/ ./
RUN npm run build

FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY backend/go.mod backend/go.sum ./backend/
COPY . .
# Overwrite the committed placeholder dist/ with the real SPA build before the
# Go build so go:embed picks up a bootable app.
COPY --from=ui /ui/dist ./backend/ui/dist
RUN cd backend && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/lcatd ./cmd/lcatd

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/lcatd /lcatd
EXPOSE 8080
ENTRYPOINT ["/lcatd"]
