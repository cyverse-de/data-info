#!groovy
def repo = "data-info"
def dockerUser = "discoenv"

node {
    slackJobDescription = "job '${env.JOB_NAME} [${env.BUILD_NUMBER}]' (${env.BUILD_URL})"
    try {
        stage "Build"
        checkout scm

        git_commit = sh(returnStdout: true, script: "git rev-parse HEAD").trim()
        echo git_commit

        version = sh(returnStdout: true, script: 'grep "(defproject" project.clj | sed -E \'s/^[^"]*"([^"]+)".*$/\\1/\'').trim()
        echo version

        dockerRepo = "test-${env.BUILD_TAG}"

        sh "docker build --rm --build-arg git_commit=${git_commit} --build-arg version=${version} -t ${dockerRepo} ."


        try {
            stage "Test"
            try {
                dockerTestRunner = "test-${env.BUILD_TAG}"
                sh "docker run --rm --name ${dockerTestRunner} -v \$(pwd)/test2junit:/usr/src/app/test2junit --entrypoint 'lein' ${dockerRepo} test2junit"
            } finally {
                junit 'test2junit/xml/*.xml'

                dockerTestCleanup = "test-cleanup-${env.BUILD_TAG}"
                sh "docker run --rm --name ${dockerTestCleanup} -v \$(pwd):/build -w /build alpine rm -r test2junit"
            }

            stage "Docker Push"
            dockerPushRepo = "${dockerUser}/${repo}:${env.BRANCH_NAME}"
            sh "docker tag ${dockerRepo} ${dockerPushRepo}"
            sh "docker push ${dockerPushRepo}"
        } finally {
            sh returnStatus: true, script: "docker kill ${dockerTestRunner}"
            sh returnStatus: true, script: "docker rm ${dockerTestRunner}"

            sh returnStatus: true, script: "docker kill ${dockerTestCleanup}"
            sh returnStatus: true, script: "docker rm ${dockerTestCleanup}"

            sh returnStatus: true, script: "docker rmi ${dockerRepo}"
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
