FROM clojure:alpine

RUN apk add --update git && \
    rm -rf /var/cache/apk

VOLUME ["/etc/iplant/de"]

ARG git_commit=unknown
ARG version=unknown
LABEL org.iplantc.de.data-info.git-ref="$git_commit" \
      org.iplantc.de.data-info.version="$version"

COPY . /usr/src/app
COPY conf/main/logback.xml /usr/src/app/logback.xml

WORKDIR /usr/src/app

RUN lein uberjar && \
    cp target/data-info-standalone.jar .

RUN ln -s "/usr/bin/java" "/bin/data-info"

ENTRYPOINT ["data-info", "-Dlogback.configurationFile=/etc/iplant/de/logging/data-info-logging.xml", "-cp", ".:data-info-standalone.jar", "data_info.core"]
CMD ["--help"]
