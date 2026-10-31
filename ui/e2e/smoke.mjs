// The UI end to end in headless Chrome, against a real canon engine (the
// Go test api/e2e_test.go starts one on in-memory buckets and runs this):
// the library loads, an item's title is edited and saved, a plan is made,
// and it's applied. Exits non-zero, with what it saw, on any failure.
//
//   node e2e/smoke.mjs <url with #token=...> <chrome executable>
import puppeteer from "puppeteer-core";

const [url, chrome] = process.argv.slice(2);
const browser = await puppeteer.launch({ executablePath: chrome, headless: true, args: ["--no-sandbox"] });
const page = await browser.newPage();
const logs = [];
page.on("console", (m) => logs.push(`console.${m.type()}: ${m.text()}`));
page.on("pageerror", (e) => logs.push(`pageerror: ${e.message}`));
const step = async (name, fn) => {
  try {
    await fn();
    console.log(`ok   ${name}`);
  } catch (e) {
    console.log(`FAIL ${name}: ${e.message}`);
    console.log((await page.content()).slice(0, 2000));
    console.log(logs.join("\n"));
    await browser.close();
    process.exit(1);
  }
};
const text = (t, timeout = 15000) =>
  page.waitForFunction((t) => document.body.innerText.includes(t), { timeout }, t);

await step("library loads", async () => {
  await page.goto(url);
  await page.waitForSelector(".card a", { timeout: 15000 });
});
await step("the token left the address bar", async () => {
  if (page.url().includes("token=")) throw new Error(page.url());
});
await step("library thumbnails display", async () => {
  // Local images come through blob: URLs; the CSP must allow them.
  await page.waitForFunction(() => [...document.querySelectorAll(".card img")].some((i) => i.complete && i.naturalWidth > 0), { timeout: 15000 });
});
await step("open the first item", async () => {
  await page.click(".card a");
  await page.waitForSelector("input.big");
});
await step("edit its title and see it saved", async () => {
  await page.click("input.big", { clickCount: 3 });
  await page.type("input.big", "Edited in the browser");
  await text("Saved to the working manifest");
});
await step("make a plan", async () => {
  await page.keyboard.press("Escape");
  await page.keyboard.press("p");
  await page.waitForFunction(() => [...document.querySelectorAll("button")].some((b) => b.textContent === "Make plan"));
  await page.evaluate(() => [...document.querySelectorAll("button")].find((b) => b.textContent === "Make plan").click());
  await text("Apply ", 60000);
});
await step("apply it", async () => {
  await page.evaluate(() => [...document.querySelectorAll("button")].find((b) => b.textContent.startsWith("Apply ")).click());
  await text("Applied.", 60000);
});
await step("plan again: nothing to do", async () => {
  await page.evaluate(() => [...document.querySelectorAll("button")].find((b) => b.textContent === "Plan again").click());
  await text("No changes.", 60000);
});
await step("surfaces and jobs render", async () => {
  await page.goto(url.split("#")[0] + "#/surfaces");
  await page.waitForSelector(".preview, .surfaces", { timeout: 15000 });
  await page.goto(url.split("#")[0] + "#/jobs");
  await page.waitForSelector("table", { timeout: 15000 });
});
if (logs.some((l) => l.startsWith("pageerror"))) {
  console.log(logs.join("\n"));
  process.exit(1);
}
await browser.close();
