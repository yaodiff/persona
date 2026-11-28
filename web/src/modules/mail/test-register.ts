import { registerModule, loadModule } from "../../core/module-host";
import { mail } from "./module";

registerModule(mail);
await loadModule(mail.id);
