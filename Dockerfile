FROM golang:1.18 AS build
WORKDIR /pxier_maintainer
COPY . .
RUN go mod tidy &&  \
    go mod vendor && \
    go build -o pxier_maintainer && \
    cp config.example.yaml config.yaml

FROM ubuntu:22.04 AS run
COPY --from=build /pxier_maintainer/pxier_maintainer .
COPY --from=build /pxier_maintainer/config.yaml .
CMD ["./pxier_maintainer"]