import jenkins.model.Jenkins
import hudson.model.*
import hudson.security.*
import jenkins.security.ApiTokenProperty
import org.jenkinsci.plugins.workflow.job.WorkflowJob
import org.jenkinsci.plugins.workflow.cps.CpsFlowDefinition

// Bootstrap only an empty Jenkins home. Preserve later UI/account changes.
def instance = Jenkins.get()
def marker = new File(instance.rootDir, '.grpctest-initialized')
if (marker.exists()) { return }
if (instance.securityRealm != SecurityRealm.NO_AUTHENTICATION) {
    throw new IllegalStateException('Existing Jenkins security configuration found; refusing to replace it.')
}
def realm = new HudsonPrivateSecurityRealm(false)
realm.createAccount('admin', new File('/run/secrets/jenkins_admin_password').text.trim())
def actionsUser = realm.createAccount('actions', new File('/run/secrets/jenkins_actions_password').text.trim())
instance.setSecurityRealm(realm)
def strategy = new GlobalMatrixAuthorizationStrategy()
strategy.add(Jenkins.ADMINISTER, 'admin')
strategy.add(Jenkins.READ, 'actions')
strategy.add(Item.READ, 'actions')
strategy.add(Item.BUILD, 'actions')
strategy.add(Item.CANCEL, 'actions')
instance.setAuthorizationStrategy(strategy)
instance.setNumExecutors(1)
instance.setSlaveAgentPort(-1)
def job = instance.createProject(WorkflowJob.class, 'grpctest-deploy')
job.setDefinition(new CpsFlowDefinition(new File('/usr/share/jenkins/ref/grpctest.Jenkinsfile').text, true))
job.addProperty(new ParametersDefinitionProperty(
    new ChoiceParameterDefinition('GROUP', ['rpc', 'web'] as String[], 'Which repository to deploy'),
    new StringParameterDefinition('REVISION', '', 'Full 40-character Git commit SHA'),
    new BooleanParameterDefinition('PRELOADED', false, 'Use already imported immutable image IDs; verify source/revision/service labels'),
    new TextParameterDefinition('IMAGES_JSON', '{}', 'Service names mapped to GHCR image digests'),
    new StringParameterDefinition('GHCR_USER', '', 'Registry username for this release'),
    new PasswordParameterDefinition('GHCR_TOKEN', '', 'Short-lived workflow token; never a stored PAT')
))
job.save()
def token = actionsUser.getProperty(ApiTokenProperty.class).tokenStore.generateNewToken('github-actions')
actionsUser.save()
def tokenFile = new File(System.getenv('DEPLOY_ROOT') + '/runtime/secrets/jenkins_actions_token')
tokenFile.text = token.plainValue + '\n'
tokenFile.setReadable(false, false)
tokenFile.setReadable(true, true)
tokenFile.setWritable(false, false)
tokenFile.setWritable(true, true)
instance.save()
marker.text = 'initialized\n'
