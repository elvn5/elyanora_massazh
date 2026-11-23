FROM golang:1.26-alpine AS build
WORKDIR /src
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /server .
FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && addgroup -S app && adduser -S app -G app
COPY --from=build /server /server
# Server deploy scripts, installed by the deploy-tools service on each release.
COPY deploy/release.sh deploy/ssh-gateway.sh deploy/install-tools.sh /deploy-tools/
USER app
EXPOSE 8080
ENTRYPOINT ["/server"]
