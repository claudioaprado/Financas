// Financas — Azure infrastructure (AD-8): Container Apps + Azure Database for
// PostgreSQL Flexible Server, single image, config/secrets via environment only.
//
// The container registry is created by deploy.sh BEFORE this template (so the
// image can be built and pushed first) and referenced here as `existing`.
//
// Deploy:  az deployment group create -g <rg> --template-file main.bicep \
//            --parameters acrName=<acr> image=<acr>.azurecr.io/financas:<tag> ...

@description('Location for all resources.')
param location string = resourceGroup().location

@description('Short, lowercase name prefix for resource names.')
@minLength(3)
@maxLength(12)
param namePrefix string = 'financas'

@description('Name of the (already-created) Azure Container Registry.')
param acrName string

@description('Container image reference (registry/repo:tag) for the app.')
param image string

@description('PostgreSQL Flexible Server major version. Use a version GA on Flexible Server (18 may not be available yet; the app has no schema, so 16 is safe).')
@allowed([
  '15'
  '16'
  '17'
])
param postgresVersion string = '16'

@description('PostgreSQL administrator login.')
param pgAdminUser string = 'financas'

@secure()
@description('PostgreSQL administrator password.')
param pgAdminPassword string

@secure()
@description('Session signing secret.')
param sessionSecret string

@description('The single owner username (shown in the greeting; used for login).')
param ownerUsername string = 'owner'

@secure()
@description('Owner argon2id PHC password hash (from `make hashpw`).')
param ownerPasswordHash string

@description('Replica bounds. Keep 1/1 with the in-memory session store (Story 1.3).')
param minReplicas int = 1
param maxReplicas int = 1

var pgServerName = '${namePrefix}-pg-${uniqueString(resourceGroup().id)}'
var logName = '${namePrefix}-logs'
var envName = '${namePrefix}-env'
var appName = '${namePrefix}-app'
var databaseName = 'financas'

resource acr 'Microsoft.ContainerRegistry/registries@2023-07-01' existing = {
  name: acrName
}

resource logs 'Microsoft.OperationalInsights/workspaces@2022-10-01' = {
  name: logName
  location: location
  properties: {
    sku: {
      name: 'PerGB2018'
    }
    retentionInDays: 30
  }
}

resource pg 'Microsoft.DBforPostgreSQL/flexibleServers@2022-12-01' = {
  name: pgServerName
  location: location
  sku: {
    name: 'Standard_B1ms'
    tier: 'Burstable'
  }
  properties: {
    version: postgresVersion
    administratorLogin: pgAdminUser
    administratorLoginPassword: pgAdminPassword
    storage: {
      storageSizeGB: 32
    }
    backup: {
      backupRetentionDays: 7
      geoRedundantBackup: 'Disabled'
    }
    highAvailability: {
      mode: 'Disabled'
    }
    // Public network access is the default (no VNet delegation); the firewall
    // rule below scopes reachability to Azure-internal services.
  }
}

resource pgDatabase 'Microsoft.DBforPostgreSQL/flexibleServers/databases@2022-12-01' = {
  parent: pg
  name: databaseName
  properties: {
    charset: 'UTF8'
    collation: 'en_US.utf8'
  }
}

// Allow Azure-internal services (Container Apps egress) to reach the server.
// The 0.0.0.0 rule is Azure's "Allow public access from Azure services" marker.
resource pgAllowAzure 'Microsoft.DBforPostgreSQL/flexibleServers/firewallRules@2022-12-01' = {
  parent: pg
  name: 'AllowAzureServices'
  properties: {
    startIpAddress: '0.0.0.0'
    endIpAddress: '0.0.0.0'
  }
}

resource env 'Microsoft.App/managedEnvironments@2024-03-01' = {
  name: envName
  location: location
  properties: {
    appLogsConfiguration: {
      destination: 'log-analytics'
      logAnalyticsConfiguration: {
        customerId: logs.properties.customerId
        sharedKey: logs.listKeys().primarySharedKey
      }
    }
  }
}

var databaseUrl = 'postgres://${pgAdminUser}:${pgAdminPassword}@${pg.properties.fullyQualifiedDomainName}:5432/${databaseName}?sslmode=require'

resource app 'Microsoft.App/containerApps@2024-03-01' = {
  name: appName
  location: location
  dependsOn: [
    pgDatabase
    pgAllowAzure
  ]
  properties: {
    managedEnvironmentId: env.id
    configuration: {
      // Single revision so a new image fully starts (and runs migrations on
      // boot) before receiving traffic; with min=max=1 there is one migrator.
      activeRevisionsMode: 'Single'
      ingress: {
        external: true
        targetPort: 8080
        transport: 'auto'
        allowInsecure: false
      }
      registries: [
        {
          server: acr.properties.loginServer
          username: acr.listCredentials().username
          passwordSecretRef: 'acr-password'
        }
      ]
      // No secret is baked into the image (AC #2): all sensitive values are
      // Container App secrets injected as env vars at runtime.
      secrets: [
        {
          name: 'acr-password'
          value: acr.listCredentials().passwords[0].value
        }
        {
          name: 'database-url'
          value: databaseUrl
        }
        {
          name: 'session-secret'
          value: sessionSecret
        }
        {
          name: 'owner-password-hash'
          value: ownerPasswordHash
        }
      ]
    }
    template: {
      containers: [
        {
          name: 'financas'
          image: image
          resources: {
            cpu: json('0.25')
            memory: '0.5Gi'
          }
          env: [
            {
              name: 'PORT'
              value: '8080'
            }
            {
              name: 'SECURE_COOKIES'
              value: 'true'
            }
            {
              name: 'OWNER_USERNAME'
              value: ownerUsername
            }
            {
              name: 'DATABASE_URL'
              secretRef: 'database-url'
            }
            {
              name: 'SESSION_SECRET'
              secretRef: 'session-secret'
            }
            {
              name: 'OWNER_PASSWORD_HASH'
              secretRef: 'owner-password-hash'
            }
          ]
        }
      ]
      scale: {
        minReplicas: minReplicas
        maxReplicas: maxReplicas
      }
    }
  }
}

output appUrl string = 'https://${app.properties.configuration.ingress.fqdn}'
output appFqdn string = app.properties.configuration.ingress.fqdn
output pgFqdn string = pg.properties.fullyQualifiedDomainName
output acrLoginServer string = acr.properties.loginServer
