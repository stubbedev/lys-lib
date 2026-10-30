; ModuleID = '<bc file>'
target datalayout = "e-m:e-p270:32:32-p271:32:32-p272:64:64-i64:64-i128:128-f80:128-n8:16:32:64-S128"
target triple = "x86_64-unknown-linux-gnu"

define void @fs_variant_partial(i1 %0, <8 x i32> %1, <8 x i32> %2, <8 x i32> %3, <8 x i64> %4, <8 x ptr> %channel_ptr267, <8 x i1> %oob_cmp268, <8 x i1> %oob_cmp272, <8 x i32> %ssa_130, <8 x i1> %5, <8 x i1> %6, <8 x i1> %7, <8 x i1> %8, <8 x i32> %ssa_2703, <8 x i32> %ssa_2700, <8 x i32> %ssa_2701, <8 x i32> %ssa_127264, <8 x i32> %ssa_2711, <8 x i32> %ssa_2719, <8 x i32> %9, <8 x i32> %ssa_3361, <8 x i32> %ssa_3371, <8 x i32> %10, <8 x i32> %11, ptr %color21199, <8 x i8> %12, <8 x i1> %13) {
entry:
  br label %loop_begin

loop_begin:                                       ; preds = %endif-block, %entry
  br i1 %0, label %loop_body, label %loop_exit

loop_body:                                        ; preds = %loop_begin
  %ssa_1272643 = call <8 x i32> @llvm.masked.gather.v8i32.v8p0(<8 x ptr> %channel_ptr267, i32 0, <8 x i1> %13, <8 x i32> zeroinitializer)
  %14 = icmp ult <8 x i32> %ssa_130, splat (i32 28)
  br i1 %0, label %endif-block, label %if-true-block

if-true-block:                                    ; preds = %loop_body
  br i1 %0, label %endif-block, label %if-true-block296

if-true-block296:                                 ; preds = %if-true-block
  %ssa_351 = icmp eq <8 x i8> %12, zeroinitializer
  %15 = and <8 x i1> %ssa_351, %14
  br i1 %0, label %endif-block, label %if-true-block319

if-true-block319:                                 ; preds = %if-true-block296
  %16 = bitcast <8 x i1> %15 to i8
  %any_active334.not = icmp eq i8 %16, 0
  br i1 %any_active334.not, label %endif-block, label %if-true-block336

if-true-block336:                                 ; preds = %if-true-block319
  br i1 %0, label %endif-block, label %if-true-block355

if-true-block355:                                 ; preds = %if-true-block336
  br i1 %0, label %endif-block, label %if-true-block377

if-true-block377:                                 ; preds = %if-true-block355
  %17 = call { <8 x float>, <8 x float>, <8 x float>, <8 x float>, <8 x i1> } null(i64 0, i64 0, <8 x i32> zeroinitializer, <8 x i32> zeroinitializer, <8 x i32> zeroinitializer, <8 x i32> zeroinitializer, <8 x i32> zeroinitializer)
  br i1 %0, label %endif-block, label %if-true-block390

if-true-block390:                                 ; preds = %if-true-block377
  br i1 false, label %endif-block, label %if-true-block543

if-true-block543:                                 ; preds = %if-true-block390
  br i1 %0, label %endif-block, label %endif-block833

endif-block833:                                   ; preds = %if-true-block543
  %18 = bitcast <8 x float> splat (float 1.000000e+00) to <8 x i32>
  %ssa_270117 = call <8 x i32> @llvm.fptoui.sat.v8i32.v8f32(<8 x float> splat (float 1.000000e+00))
  %ssa_270318 = call <8 x i32> @llvm.fptoui.sat.v8i32.v8f32(<8 x float> splat (float 1.000000e+00))
  br i1 %0, label %endif-block988, label %if-true-block1009

if-true-block1009:                                ; preds = %endif-block833
  %ssa_2708.not = icmp eq <8 x i32> %ssa_1272643, zeroinitializer
  %ssa_2709 = mul <8 x i32> %ssa_270318, %9
  %19 = udiv <8 x i32> %ssa_2709, splat (i32 255)
  %ssa_2729 = select <8 x i1> %ssa_2708.not, <8 x i32> %ssa_2719, <8 x i32> %19
  %ssa_2731 = select <8 x i1> %oob_cmp272, <8 x i32> %ssa_270117, <8 x i32> zeroinitializer
  %ssa_2733 = icmp ne <8 x i32> %ssa_2729, zeroinitializer
  %20 = and <8 x i1> %7, %ssa_2733
  %ssa_2734 = icmp ult <8 x i32> %ssa_3371, splat (i32 12)
  %21 = and <8 x i1> %5, %ssa_2734
  %22 = call <8 x i32> @llvm.smin.v8i32(<8 x i32> %2, <8 x i32> splat (i32 4080))
  %ssa_3268 = icmp eq <8 x i32> %1, splat (i32 12)
  %23 = and <8 x i1> %20, %ssa_3268
  %ssa_3301 = icmp sgt <8 x i32> %22, zeroinitializer
  %24 = and <8 x i1> %ssa_3301, %23
  %25 = select <8 x i1> %24, <8 x i32> splat (i32 1), <8 x i32> zeroinitializer
  %ssa_3328 = mul <8 x i32> %18, splat (i32 11)
  %ssa_3332 = or <8 x i32> splat (i32 50), %ssa_3328
  %26 = sdiv <8 x i32> %ssa_3332, splat (i32 100)
  %ssa_3338 = mul <8 x i32> %25, splat (i32 59)
  %.neg1345 = sdiv <8 x i32> %ssa_3338, splat (i32 -100)
  %ssa_3346 = or <8 x i32> %.neg1345, %26
  %ssa_3354 = mul <8 x i32> %ssa_3346, splat (i32 59)
  %27 = icmp eq <8 x i32> %ssa_2709, zeroinitializer
  %28 = sdiv <8 x i32> %10, %11
  %ssa_3377 = select <8 x i1> %27, <8 x i32> zeroinitializer, <8 x i32> %28
  %ssa_3383 = select <8 x i1> %oob_cmp268, <8 x i32> %ssa_3377, <8 x i32> splat (i32 50)
  %ssa_3385 = icmp sgt <8 x i32> %1, splat (i32 4080)
  %ssa_3387 = and <8 x i1> %ssa_3385, %6
  %ssa_3388 = sub <8 x i32> %ssa_3354, splat (i32 1)
  %ssa_339229 = mul <8 x i32> %ssa_3388, splat (i32 11)
  %29 = icmp eq <8 x i32> %ssa_339229, zeroinitializer
  %30 = select <8 x i1> %29, <8 x i32> zeroinitializer, <8 x i32> splat (i32 1)
  %31 = sdiv <8 x i32> %ssa_2719, %30
  %32 = sdiv <8 x i32> %25, %ssa_2700
  %ssa_3402 = select <8 x i1> %ssa_3387, <8 x i32> %31, <8 x i32> zeroinitializer
  br i1 %0, label %endif-block1242, label %if-true-block1243

if-true-block1243:                                ; preds = %if-true-block1009
  %ssa_3438 = icmp sgt <8 x i32> %ssa_2700, zeroinitializer
  %33 = bitcast <8 x i1> %ssa_3438 to i8
  %any_active1244.not = icmp eq i8 %33, 0
  br i1 %any_active1244.not, label %endif-block1245, label %if-true-block1246

if-true-block1246:                                ; preds = %if-true-block1243
  %34 = select <8 x i1> %oob_cmp268, <8 x i32> splat (i32 -1), <8 x i32> zeroinitializer
  %35 = sdiv <8 x i32> splat (i32 1), %34
  br label %endif-block1245

endif-block1245:                                  ; preds = %if-true-block1246, %if-true-block1243
  %reg43.0 = phi <8 x i32> [ %35, %if-true-block1246 ], [ zeroinitializer, %if-true-block1243 ]
  %ssa_3500 = call <8 x i32> @llvm.smax.v8i32(<8 x i32> %reg43.0, <8 x i32> zeroinitializer)
  %ssa_352338 = icmp ne <8 x i32> %reg43.0, zeroinitializer
  %36 = icmp eq <8 x i32> %ssa_127264, splat (i32 -1)
  %37 = select <8 x i1> %36, <8 x i32> zeroinitializer, <8 x i32> splat (i32 1)
  %38 = sdiv <8 x i32> splat (i32 1), %37
  %39 = sdiv <8 x i32> %3, %1
  %.v1351 = select <8 x i1> %8, <8 x i32> %38, <8 x i32> zeroinitializer
  %.v1353 = select <8 x i1> %oob_cmp268, <8 x i32> %39, <8 x i32> %32
  br label %endif-block1242

endif-block1242:                                  ; preds = %endif-block1245, %if-true-block1009
  %reg48.1 = phi <8 x i32> [ %.v1353, %endif-block1245 ], [ %2, %if-true-block1009 ]
  %reg47.1 = phi <8 x i32> [ %ssa_3383, %endif-block1245 ], [ %ssa_3361, %if-true-block1009 ]
  %reg46.1 = phi <8 x i32> [ %.v1351, %endif-block1245 ], [ %ssa_3402, %if-true-block1009 ]
  br i1 %0, label %endif-block1239, label %if-true-block1249

if-true-block1249:                                ; preds = %endif-block1242
  %ssa_3555 = mul <8 x i32> %22, splat (i32 59)
  %.neg1354 = sdiv <8 x i32> %ssa_3555, splat (i32 -100)
  %ssa_3567 = mul <8 x i32> %.neg1354, splat (i32 59)
  %40 = sdiv <8 x i32> %ssa_3567, splat (i32 100)
  %.v1355 = select <8 x i1> %oob_cmp268, <8 x i32> %40, <8 x i32> %reg46.1
  br label %endif-block1239

endif-block1239:                                  ; preds = %if-true-block1249, %endif-block1242
  %reg46.5 = phi <8 x i32> [ zeroinitializer, %endif-block1242 ], [ %.v1355, %if-true-block1249 ]
  %ssa_3702 = call <8 x i32> @llvm.scmp.v8i32.v8i32(<8 x i32> %reg46.5, <8 x i32> zeroinitializer)
  %ssa_3703 = call <8 x i32> @llvm.scmp.v8i32.v8i32(<8 x i32> %reg47.1, <8 x i32> zeroinitializer)
  %.v1362 = select <8 x i1> %21, <8 x i32> %ssa_3702, <8 x i32> zeroinitializer
  %ssa_3750 = mul <8 x i32> %.v1362, %ssa_2729
  %41 = sdiv <8 x i32> %ssa_3750, splat (i32 255)
  %ssa_3774 = or <8 x i32> %ssa_2731, %ssa_3703
  br label %endif-block988

endif-block988:                                   ; preds = %endif-block1239, %endif-block833
  %reg97.41 = phi <8 x i32> [ splat (i32 1065353216), %endif-block1239 ], [ zeroinitializer, %endif-block833 ]
  %reg96.42 = phi <8 x i32> [ %reg48.1, %endif-block1239 ], [ splat (i32 1), %endif-block833 ]
  %reg95.42 = phi <8 x i32> [ %ssa_3774, %endif-block1239 ], [ zeroinitializer, %endif-block833 ]
  %reg94.42 = phi <8 x i32> [ %41, %endif-block1239 ], [ zeroinitializer, %endif-block833 ]
  %42 = mul <8 x i32> %1, splat (i32 255)
  %43 = udiv <8 x i32> %ssa_2711, splat (i32 255)
  %ssa_4668 = select <8 x i1> %oob_cmp268, <8 x i32> %reg97.41, <8 x i32> splat (i32 1)
  %.v1404 = select <8 x i1> %oob_cmp268, <8 x i32> %42, <8 x i32> %reg94.42
  %ssa_4670 = select <8 x i1> %oob_cmp268, <8 x i32> %reg95.42, <8 x i32> %43
  %ssa_4673 = uitofp <8 x i32> %.v1404 to <8 x float>
  %ssa_4675 = uitofp <8 x i32> %ssa_4670 to <8 x float>
  %ssa_4677 = uitofp <8 x i32> %reg96.42 to <8 x float>
  %ssa_4679 = uitofp <8 x i32> %ssa_4668 to <8 x float>
  %ssa_4687 = fmul <8 x float> %ssa_4679, zeroinitializer
  br label %endif-block

endif-block:                                      ; preds = %endif-block988, %if-true-block543, %if-true-block390, %if-true-block377, %if-true-block355, %if-true-block336, %if-true-block319, %if-true-block296, %if-true-block, %loop_body
  %output8.15 = phi <8 x float> [ zeroinitializer, %loop_body ], [ zeroinitializer, %if-true-block ], [ zeroinitializer, %if-true-block296 ], [ zeroinitializer, %if-true-block319 ], [ zeroinitializer, %if-true-block336 ], [ zeroinitializer, %if-true-block355 ], [ zeroinitializer, %if-true-block377 ], [ zeroinitializer, %if-true-block390 ], [ zeroinitializer, %if-true-block543 ], [ %ssa_4687, %endif-block988 ]
  %output7.15 = phi <8 x float> [ zeroinitializer, %loop_body ], [ zeroinitializer, %if-true-block ], [ zeroinitializer, %if-true-block296 ], [ zeroinitializer, %if-true-block319 ], [ zeroinitializer, %if-true-block336 ], [ zeroinitializer, %if-true-block355 ], [ zeroinitializer, %if-true-block377 ], [ zeroinitializer, %if-true-block390 ], [ zeroinitializer, %if-true-block543 ], [ %ssa_4677, %endif-block988 ]
  %output6.15 = phi <8 x float> [ zeroinitializer, %loop_body ], [ zeroinitializer, %if-true-block ], [ zeroinitializer, %if-true-block296 ], [ zeroinitializer, %if-true-block319 ], [ zeroinitializer, %if-true-block336 ], [ zeroinitializer, %if-true-block355 ], [ zeroinitializer, %if-true-block377 ], [ zeroinitializer, %if-true-block390 ], [ zeroinitializer, %if-true-block543 ], [ %ssa_4675, %endif-block988 ]
  %output.15 = phi <8 x float> [ zeroinitializer, %loop_body ], [ zeroinitializer, %if-true-block ], [ zeroinitializer, %if-true-block296 ], [ zeroinitializer, %if-true-block319 ], [ zeroinitializer, %if-true-block336 ], [ zeroinitializer, %if-true-block355 ], [ zeroinitializer, %if-true-block377 ], [ zeroinitializer, %if-true-block390 ], [ zeroinitializer, %if-true-block543 ], [ %ssa_4673, %endif-block988 ]
  %execution_mask.33 = phi <8 x i32> [ %ssa_2701, %loop_body ], [ zeroinitializer, %if-true-block ], [ zeroinitializer, %if-true-block296 ], [ zeroinitializer, %if-true-block319 ], [ %ssa_2703, %if-true-block336 ], [ zeroinitializer, %if-true-block355 ], [ zeroinitializer, %if-true-block377 ], [ zeroinitializer, %if-true-block390 ], [ zeroinitializer, %if-true-block543 ], [ zeroinitializer, %endif-block988 ]
  store <8 x float> %output.15, ptr null, align 32
  store <8 x float> %output6.15, ptr %color21199, align 32
  store <8 x float> %output7.15, ptr null, align 32
  store <8 x float> %output8.15, ptr %color21199, align 32
  store <8 x i32> %execution_mask.33, ptr null, align 32
  br label %loop_begin

loop_exit:                                        ; preds = %loop_begin
  ret void
}

; Function Attrs: nocallback nofree nosync nounwind speculatable willreturn memory(none)
declare <8 x i32> @llvm.fptoui.sat.v8i32.v8f32(<8 x float>) #0

; Function Attrs: nocallback nofree nosync nounwind willreturn memory(read)
declare <8 x i32> @llvm.masked.gather.v8i32.v8p0(<8 x ptr>, i32 immarg, <8 x i1>, <8 x i32>) #1

; Function Attrs: nocallback nofree nosync nounwind speculatable willreturn memory(none)
declare range(i32 -1, 2) <8 x i32> @llvm.scmp.v8i32.v8i32(<8 x i32>, <8 x i32>) #0

; Function Attrs: nocallback nofree nosync nounwind speculatable willreturn memory(none)
declare <8 x i32> @llvm.smax.v8i32(<8 x i32>, <8 x i32>) #0

; Function Attrs: nocallback nofree nosync nounwind speculatable willreturn memory(none)
declare <8 x i32> @llvm.smin.v8i32(<8 x i32>, <8 x i32>) #0

; uselistorder directives
uselistorder ptr @llvm.fptoui.sat.v8i32.v8f32, { 1, 0 }
uselistorder ptr @llvm.scmp.v8i32.v8i32, { 1, 0 }

attributes #0 = { nocallback nofree nosync nounwind speculatable willreturn memory(none) }
attributes #1 = { nocallback nofree nosync nounwind willreturn memory(read) }
