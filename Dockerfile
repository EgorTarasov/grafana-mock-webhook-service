FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY web ./web
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/grafana-mock-webhook-service .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/grafana-mock-webhook-service /usr/local/bin/grafana-mock-webhook-service
EXPOSE 3000
ENTRYPOINT ["grafana-mock-webhook-service"]
CMD ["serve"]
