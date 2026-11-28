import type { common } from '@kit.AbilityKit';
import { healthStore } from '@kit.HealthServiceKit';

export class WorkoutAccessResult {
  state:string='';
  code:number=0;
  constructor(state:string,code:number=0) {this.state=state;this.code=code;}
}

let initializedContext:common.Context | undefined=undefined;

function workoutErrorCode(error:Object | null | undefined):number {
  if (!error || typeof error!=='object') return 0;
  const code=(error as Record<string,Object>)['code'];
  return typeof code==='number' && Number.isFinite(code) ? code : 0;
}

export async function workoutAccess(context?:common.Context):Promise<WorkoutAccessResult> {
  if (!canIUse('SystemCapability.Health.HealthStore')) return new WorkoutAccessResult('unavailable');
  try {
    // Use the static Kit import rewritten by ets-loader to @hms.health.store; dynamic import('@kit.*') is not transformed.
    const workout=healthStore.exerciseSequenceHelper?.DATA_TYPE;
    if (!workout || typeof workout.id!=='number') return new WorkoutAccessResult('unknown');
    if (context && initializedContext!==context && typeof healthStore.init==='function') {
      await healthStore.init(context);
      initializedContext=context;
    }
    const request:healthStore.AuthorizationRequest={readDataTypes:[workout],writeDataTypes:[]};
    const result=await healthStore.getAuthorizations(request);
    const readTypes=result?.readDataTypes;
    if (!Array.isArray(readTypes)) return new WorkoutAccessResult('unknown');
    const granted=readTypes.some((type:healthStore.DataType)=>!!type && type.id===workout.id);
    return new WorkoutAccessResult(granted ? 'granted' : 'not_granted');
  } catch (error) {
    return new WorkoutAccessResult('unknown',workoutErrorCode(error as Object | null | undefined));
  }
}
