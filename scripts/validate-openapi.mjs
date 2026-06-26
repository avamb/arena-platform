import yaml from 'js-yaml';
import fs from 'fs';

try {
  const d = yaml.load(fs.readFileSync('apps/backend/openapi/openapi.yaml', 'utf8'));
  const paths = Object.keys(d.paths);
  const schemas = Object.keys(d.components.schemas);
  console.log('PARSED OK', paths.length, 'paths,', schemas.length, 'schemas');
  const needPaths = [
    '/v1/operator-networks',
    '/v1/operator-networks/{id}',
    '/v1/operator-networks/{id}/archive',
    '/v1/admin/networks/{id}/users',
    '/v1/admin/networks/{id}/users/{userId}',
    '/v1/admin/networks/{id}/organizers',
    '/v1/admin/networks/{id}/organizers/{orgId}',
    '/v1/admin/networks/{id}/agents',
    '/v1/admin/networks/{id}/agents/{orgId}',
    '/v1/me',
  ];
  const needSchemas = [
    'OperatorNetwork',
    'NetworkUserAssignment',
    'NetworkOrganizationAssignment',
    'MeResponse',
  ];
  const missingPaths = needPaths.filter((p) => !paths.includes(p));
  const missingSchemas = needSchemas.filter((s) => !schemas.includes(s));
  console.log('MISSING_PATHS', JSON.stringify(missingPaths));
  console.log('MISSING_SCHEMAS', JSON.stringify(missingSchemas));
} catch (e) {
  console.error('ERR', e.message);
  process.exit(1);
}
