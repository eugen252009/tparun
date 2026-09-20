const fs = require('fs');
const vm = require('vm');
const resources = [
  { dataset: { usedBytes: '0', limitBytes: '0', limitState: 'finite' }, textContent: '' },
  { dataset: { usedBytes: '8015220', limitBytes: '5000000', limitState: 'finite' }, textContent: '' },
  { dataset: { usedBytes: '1162416786', limitBytes: '12000000000', limitState: 'finite' }, textContent: '' },
  { dataset: { usedBytes: '1000000', limitBytes: '0', limitState: 'finite' }, textContent: '' },
  { dataset: { usedBytes: '8015220', limitState: 'none' }, textContent: '8015220 B / no applicable limit' },
  { dataset: { usedBytes: 'bad', limitBytes: '5000000', limitState: 'finite' }, textContent: 'fallback' },
  { dataset: { usedBytes: '1000000', limitState: 'finite' }, textContent: '1 MB / unknown' },
];
const bytes = [
  { dataset: { bytes: '999' }, textContent: '' },
  { dataset: { bytes: '1000' }, textContent: '' },
  { dataset: { bytes: '1000000' }, textContent: '' },
  { dataset: { bytes: '1000000000' }, textContent: '' },
  { dataset: { bytes: '12000000000' }, textContent: '' },
];
const context = { document: { querySelectorAll: selector => selector === '.resource-usage' ? resources : bytes } };
vm.runInNewContext(fs.readFileSync('cmd/tparun/static/ops.js', 'utf8'), context);
const expectedResources = ['0 B / 0 B', '8.02 MB / 5.00 MB (160.3%)', '1.16 GB / 12.00 GB (9.7%)', '1.00 MB / 0 B', '8.02 MB / no applicable limit', 'fallback', '1 MB / unknown'];
const expectedBytes = ['999 B', '1.00 KB', '1.00 MB', '1.00 GB', '12.00 GB'];
for (const [i, expected] of expectedResources.entries()) if (resources[i].textContent !== expected) throw Error(`resource ${i}: ${resources[i].textContent}`);
for (const [i, expected] of expectedBytes.entries()) if (bytes[i].textContent !== expected) throw Error(`bytes ${i}: ${bytes[i].textContent}`);
const once = resources.map(e => e.textContent);
vm.runInNewContext(fs.readFileSync('cmd/tparun/static/ops.js', 'utf8'), context);
for (const [i, expected] of once.entries()) if (resources[i].textContent !== expected) throw Error(`idempotence ${i}: ${resources[i].textContent}`);
console.log('OPS RESOURCE FORMATTER QUALIFIED');
console.log('OPS RESOURCE FORMATTER QUALIFIED');
