FROM docker.io/library/golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/manager .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/manager /manager
USER nonroot:nonroot
EXPOSE 8080 8081
ENTRYPOINT ["/manager"]
