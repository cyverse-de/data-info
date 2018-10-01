#!groovy
milestone 0
node('docker') {
    slackJobDescription = "job '${env.JOB_NAME} [${env.BUILD_NUMBER}]' (${env.BUILD_URL})"
    try {
        stage "Build"
        checkout scm

        service = readProperties file: 'service.properties'

        git_commit = sh(returnStdout: true, script: "git rev-parse HEAD").trim()
        echo git_commit

        version = sh(returnStdout: true, script: 'grep "(defproject" project.clj | sed -E \'s/^[^"]*"([^"]+)".*$/\\1/\'').trim()
        echo version

        descriptive_version = sh(returnStdout: true, script: 'git describe --long --tags --dirty --always').trim()
        echo descriptive_version

        dockerRepo = "test-${env.BUILD_TAG}"
        dockerPushRepo = "${service.dockerUser}/${service.repo}:${env.BRANCH_NAME}"

        milestone 50
        sh "docker build --pull --cache-from=${dockerPushRepo} --rm --build-arg git_commit=${git_commit} --build-arg version=${version} --build-arg descriptive_version=${descriptive_version} -t ${dockerRepo} ."
        milestone 51

        image_sha = sh(returnStdout: true, script: "docker inspect -f '{{ .Config.Image }}' ${dockerRepo}").trim()
        echo image_sha

        writeFile(file: "${dockerRepo}.docker-image-sha", text: "${image_sha}")
        fingerprint "${dockerRepo}.docker-image-sha"

        dockerTestRunner = "test-${env.BUILD_TAG}"
        dockerTestCleanup = "test-cleanup-${env.BUILD_TAG}"
        dockerPusher = "push-${env.BUILD_TAG}"
        try {
            stage "Test"
            try {
                sh "docker run --rm --name ${dockerTestRunner} -v \$(pwd)/test2junit:/usr/src/app/test2junit --entrypoint 'lein' ${dockerRepo} test2junit"
            } finally {
                junit 'test2junit/xml/*.xml'

                sh "docker run --rm --name ${dockerTestCleanup} -v \$(pwd):/build -w /build alpine rm -r test2junit"
            }

            milestone 100
            stage "Docker Push"
            lock("docker-push-${dockerPushRepo}") {
              milestone 101
              sh "docker tag ${dockerRepo} ${dockerPushRepo}"
              withCredentials([[$class: 'UsernamePasswordMultiBinding', credentialsId: 'jenkins-docker-credentials', passwordVariable: 'DOCKER_PASSWORD', usernameVariable: 'DOCKER_USERNAME']]) {
                  sh """docker run -e DOCKER_USERNAME -e DOCKER_PASSWORD \\
                                   -v /var/run/docker.sock:/var/run/docker.sock \\
                                   --rm --name ${dockerPusher} \\
                                   docker:\$(docker version --format '{{ .Server.Version }}') \\
                                   sh -e -c \\
                        'docker login -u \"\$DOCKER_USERNAME\" -p \"\$DOCKER_PASSWORD\" && \\
                         docker push ${dockerPushRepo} && \\
                         docker rmi ${dockerPushRepo} && \\
                         docker logout'"""
              }
            }
        } finally {
            sh returnStatus: true, script: "docker kill ${dockerTestRunner}"
            sh returnStatus: true, script: "docker rm ${dockerTestRunner}"

            sh returnStatus: true, script: "docker kill ${dockerTestCleanup}"
            sh returnStatus: true, script: "docker rm ${dockerTestCleanup}"

            sh returnStatus: true, script: "docker kill ${dockerPusher}"
            sh returnStatus: true, script: "docker rm ${dockerPusher}"

            sh returnStatus: true, script: "docker rmi ${dockerRepo}"

            step([$class: 'hudson.plugins.jira.JiraIssueUpdater',
                    issueSelector: [$class: 'hudson.plugins.jira.selector.DefaultIssueSelector'],
                    scm: scm,
                    labels: [ "${service.repo}-${descriptive_version}" ]])
        }
    } catch (InterruptedException e) {
        currentBuild.result = "ABORTED"
        slackSend color: 'warning', message: "ABORTED: ${slackJobDescription}"
        throw e
    } catch (e) {
        currentBuild.result = "FAILED"
        sh "echo ${e}"
        slackSend color: 'danger', message: "FAILED: ${slackJobDescription}"
        throw e
    }
}
