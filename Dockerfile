FROM clojure:temurin-25-lein-trixie

WORKDIR /usr/src/app

RUN apt-get update && \
    apt-get install -y git && \
    rm -rf /var/lib/apt/lists/*

RUN ln -s "/opt/java/openjdk/bin/java" "/bin/data-info"

COPY project.clj /usr/src/app/
RUN lein deps

COPY conf/main/logback.xml /usr/src/app/
COPY . /usr/src/app

RUN lein do clean, uberjar && \
    cp target/data-info-standalone.jar .

# Pre-load the class metadata data-info needs at startup into an AOT cache, roughly halving startup
# time. shutdown-agents is needed because loading the config namespace shells out for the hostname,
# and the agent pool's non-daemon threads would otherwise hold the JVM open for their 60s keepalive,
# adding a minute to every build. It makes no difference to the long-running service.
RUN data-info -XX:AOTCacheOutput=/usr/src/app/data-info.aot \
      -Dlogback.configurationFile=/usr/src/app/logback.xml \
      -cp data-info-standalone.jar \
      clojure.main -e "(require 'data-info.core 'data-info.routes) (shutdown-agents)"

# Only the jar belongs on the classpath. This image builds in place, so the working directory is the
# whole source tree; nothing in it is needed at runtime. logback is configured by absolute path
# above, data_info.core takes its config from --config, and the resources that ship under nexml/ and
# scripts/ are packaged into the jar. Keeping the working directory off the classpath is also what
# lets the AOT cache load: the dumper refuses a non-empty directory, and a runtime classpath that
# differs from the dumped one is rejected. A missing or rejected cache only logs an error; data-info
# still starts.
ENTRYPOINT ["data-info", "-Dlogback.configurationFile=/usr/src/app/logback.xml", "-XX:AOTCache=/usr/src/app/data-info.aot", "-cp", "data-info-standalone.jar", "data_info.core"]
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
