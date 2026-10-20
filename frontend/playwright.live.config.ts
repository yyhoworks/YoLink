import { defineConfig } from '@playwright/test';

const baseURL=process.env.YOLINK_TEST_BASE;
if(!baseURL||!/^http:\/\/127\.0\.0\.1:\d+$/.test(baseURL))throw new Error('Live tests require the isolated Go loopback fixture');
export default defineConfig({
 testDir:'./tests/live',workers:1,reporter:'list',outputDir:'../.impeccable/review/reliability-live',
 use:{baseURL,browserName:'chromium',channel:'msedge',locale:'zh-CN',timezoneId:'Asia/Shanghai',reducedMotion:'reduce',trace:'retain-on-failure'},
});
