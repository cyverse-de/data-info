FROM discoenv/clojure-base:master

ENV CONF_TEMPLATE=/usr/src/app/data-info.properties.tmpl
ENV CONF_FILENAME=data-info.properties
ENV PROGRAM=data-info

VOLUME ["/etc/iplant/de"]

COPY project.clj /usr/src/app/
RUN lein deps

COPY conf/main/logback.xml /usr/src/app/
COPY . /usr/src/app

RUN lein uberjar && \
    cp target/data-info-standalone.jar .

RUN ln -s "/usr/bin/java" "/bin/data-info"

ENTRYPOINT ["run-service", "-Dlogback.configurationFile=/etc/iplant/de/logging/data-info-logging.xml", "-cp", ".:data-info-standalone.jar", "data_info.core"]
CMD ["--help"]

ARG git_commit=unknown
ARG version=unknown
ARG descriptive_version=unknown

LABEL org.cyverse.git-ref="$git_commit"
LABEL org.cyverse.version="$version"
LABEL org.cyverse.descriptive-version="$descriptive_version"
LABEL org.label-schema.vcs-ref="$git_commit"
LABEL org.label-schema.vcs-url="https://github.com/cyverse-de/data-info"
LABEL org.label-schema.version="$descriptive_version"
